package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

// 代表不同负载均衡策略
type SelectMode int

const (
	RandomSelect     SelectMode = iota // 随机选择
	RoundRobinSelect                   //轮询调度(就是按顺序)
)

type Discovery interface {
	Refresh() error                      //从注册中心更新服务列表
	Update(servers []string) error       //传入服务手动更新列表
	Get(mode SelectMode) (string, error) //根据传入的策略选择一个服务实例
	GetAll() ([]string, error)           //返回所有服务实例
}
type MultiServersDiscovery struct {
	mu      sync.Mutex
	r       *rand.Rand //随机因子
	servers []string
	index   int //轮询调度的下标
}

func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

var _ Discovery = (*MultiServersDiscovery)(nil)

func (m *MultiServersDiscovery) Refresh() error {
	return nil
}
func (m *MultiServersDiscovery) Update(servers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers = servers
	return nil
}
func (m *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		return m.servers[m.r.Intn(n)], nil
	case RoundRobinSelect:
		s := m.servers[m.index%n]
		m.index = (m.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}
func (m *MultiServersDiscovery) GetAll() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := make([]string, len(m.servers), len(m.servers))
	copy(s, m.servers)
	return s, nil
}
