package weight

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	mdns "github.com/miekg/dns"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/iface"
)

// NacosWeightProvider reads per-FQDN traffic-splitting weights from Nacos via
// push (ListenConfig) with an initial pull (GetConfig) on first access.
//
// DataID format: {prefix}{fqdn}:{typeName}   e.g. "dns_weights:api.example.com.:A"
// Value format:  JSON {"rdata": weight}       e.g. {"1.2.3.4": 70, "5.6.7.8": 30}
type NacosWeightProvider struct {
	client config_client.IConfigClient
	group  string
	prefix string
	log    *zap.Logger

	mu      sync.RWMutex
	weights map[string]map[string]int // cacheKey → {rdata: weight}
	watched sync.Map                  // cacheKey → struct{}: prevents duplicate listeners
}

var _ iface.WeightProvider = (*NacosWeightProvider)(nil)

// NewNacosWeightProvider connects to Nacos and returns a provider.
// Returns an error if the Nacos address is invalid or unreachable.
func NewNacosWeightProvider(cfg config.NacosConfig, log *zap.Logger) (*NacosWeightProvider, error) {
	client, err := newNacosClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("nacos: client: %w", err)
	}
	return &NacosWeightProvider{
		client:  client,
		group:   cfg.Group,
		prefix:  cfg.DataIDPrefix,
		log:     log,
		weights: make(map[string]map[string]int),
	}, nil
}

// GetWeights returns the cached weight map for (fqdn, qtype).
// clientIP is reserved for future geo-routing; currently ignored.
// Registers a Nacos listener lazily on first call for an unknown key.
// Returns nil when no dynamic weights are configured.
func (p *NacosWeightProvider) GetWeights(fqdn string, qtype uint16, _ net.IP) map[string]int {
	key := cacheKey(fqdn, qtype)

	// lazy-register: only one goroutine wins the LoadOrStore race per key
	if _, loaded := p.watched.LoadOrStore(key, struct{}{}); !loaded {
		go p.watch(key, fqdn, qtype)
	}

	p.mu.RLock()
	ws := p.weights[key]
	p.mu.RUnlock()
	return ws
}

// Start pre-fetches weights and registers listeners for every FQDN/type pair
// currently in zoneStore. Call once after LoadAll, before DNS serving starts.
func (p *NacosWeightProvider) Start(zoneStore iface.ZoneStore) {
	snap := zoneStore.Snapshot()
	for _, zone := range snap {
		for rkey := range zone.Records {
			key := cacheKey(rkey.Name, rkey.Qtype)
			if _, loaded := p.watched.LoadOrStore(key, struct{}{}); !loaded {
				p.watch(key, rkey.Name, rkey.Qtype) // synchronous during startup
			}
		}
	}
}

// watch performs the initial GetConfig pull and registers the ListenConfig push.
// Called once per (fqdn, qtype) pair; duplicate calls are prevented by LoadOrStore.
func (p *NacosWeightProvider) watch(key, fqdn string, qtype uint16) {
	dataID := p.dataID(fqdn, qtype)

	// initial pull
	content, err := p.client.GetConfig(vo.ConfigParam{DataId: dataID, Group: p.group})
	if err != nil {
		p.log.Warn("nacos: GetConfig failed",
			zap.String("dataId", dataID), zap.Error(err))
	} else if content != "" {
		p.update(key, dataID, content)
	}

	// register push listener — stays active for the lifetime of the process
	if err := p.client.ListenConfig(vo.ConfigParam{
		DataId:   dataID,
		Group:    p.group,
		OnChange: func(_, _, _, data string) { p.update(key, dataID, data) },
	}); err != nil {
		p.log.Error("nacos: ListenConfig failed",
			zap.String("dataId", dataID), zap.Error(err))
	}
}

// update parses the JSON weight map and stores it.
func (p *NacosWeightProvider) update(key, dataID, data string) {
	var ws map[string]int
	if err := json.Unmarshal([]byte(data), &ws); err != nil {
		p.log.Error("nacos: malformed weight JSON",
			zap.String("dataId", dataID), zap.String("data", data), zap.Error(err))
		return
	}

	p.mu.Lock()
	if len(ws) == 0 {
		delete(p.weights, key)
	} else {
		p.weights[key] = ws
	}
	p.mu.Unlock()

	p.log.Info("nacos: weights updated",
		zap.String("dataId", dataID), zap.Int("entries", len(ws)))
}

// dataID builds "prefix + fqdn + : + typeName".
func (p *NacosWeightProvider) dataID(fqdn string, qtype uint16) string {
	return p.prefix + fqdn + ":" + typeStr(qtype)
}

// cacheKey builds the in-memory map key "fqdn:typeName".
func cacheKey(fqdn string, qtype uint16) string {
	return fqdn + ":" + typeStr(qtype)
}

func typeStr(qtype uint16) string {
	if s := mdns.TypeToString[qtype]; s != "" {
		return s
	}
	return fmt.Sprintf("TYPE%d", qtype)
}

// newNacosClient creates a Nacos config client from NacosConfig.
func newNacosClient(cfg config.NacosConfig) (config_client.IConfigClient, error) {
	idx := strings.LastIndex(cfg.Addr, ":")
	if idx < 0 {
		return nil, fmt.Errorf("invalid nacos addr %q, expected host:port", cfg.Addr)
	}
	host := cfg.Addr[:idx]
	port, err := strconv.ParseUint(cfg.Addr[idx+1:], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid nacos port in %q: %w", cfg.Addr, err)
	}

	sc := []constant.ServerConfig{*constant.NewServerConfig(host, port)}
	cc := *constant.NewClientConfig(
		constant.WithNamespaceId(cfg.Namespace),
		constant.WithTimeoutMs(5000),
		constant.WithNotLoadCacheAtStart(true),
		constant.WithLogDir("/tmp/nacos/log"),
		constant.WithCacheDir("/tmp/nacos/cache"),
		constant.WithLogLevel("warn"),
		constant.WithUsername(cfg.Username),
		constant.WithPassword(cfg.Password),
	)
	return clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: sc,
	})
}
