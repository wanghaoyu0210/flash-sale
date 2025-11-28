package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"seckill-system/config"
	"sort"
	"sync"
	"time"

	"github.com/go-zookeeper/zk"
)

type ZooKeeperService struct {
	conn      *zk.Conn
	config    *config.Config
	watchLock sync.RWMutex
	watchers  map[string][]chan []byte
}

func NewZooKeeperService(cfg *config.Config) (*ZooKeeperService, error) {
	conn, _, err := zk.Connect(cfg.ZooKeeper.Hosts, cfg.ZooKeeper.Timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ZooKeeper: %v", err)
	}

	service := &ZooKeeperService{
		conn:     conn,
		config:   cfg,
		watchers: make(map[string][]chan []byte),
	}

	// 初始化ZooKeeper路径
	if err := service.initPaths(); err != nil {
		return nil, err
	}

	service.displayChildrenInfo()

	return service, nil
}

// 初始化必要的ZooKeeper路径
func (s *ZooKeeperService) initPaths() error {
	paths := []string{
		s.config.ZooKeeper.RootPath,
		path.Join(s.config.ZooKeeper.RootPath, "locks"),
		path.Join(s.config.ZooKeeper.RootPath, "config"),
		path.Join(s.config.ZooKeeper.RootPath, "services"),
		path.Join(s.config.ZooKeeper.RootPath, "stock"),
	}

	for _, p := range paths {
		exists, _, err := s.conn.Exists(p)
		if err != nil {
			return err
		}
		if !exists {
			_, err := s.conn.Create(p, []byte{}, 0, zk.WorldACL(zk.PermAll))
			if err != nil && err != zk.ErrNodeExists {
				return err
			}
		}
	}

	return nil
}

// 分布式锁实现
func (s *ZooKeeperService) AcquireLock(lockPath string, timeout time.Duration) (string, error) {
	lockNode := path.Join(s.config.ZooKeeper.RootPath, "locks", lockPath)

	// 创建临时顺序节点
	p, err := s.conn.CreateProtectedEphemeralSequential(
		lockNode,
		[]byte{},
		zk.WorldACL(zk.PermAll),
	)
	if err != nil {
		return "", err
	}

	// 获取锁的所有竞争者
	children, _, err := s.conn.Children(path.Join(s.config.ZooKeeper.RootPath, "locks", lockPath))
	if err != nil {
		return "", err
	}

	// 检查是否获得锁（序号最小）
	if s.isLockAcquired(p, children) {
		return p, nil
	}

	// 设置超时
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 监听前一个节点的删除事件
	for {
		prevNode := s.getPreviousNode(p, children)
		if prevNode == "" {
			return p, nil
		}

		exists, _, ch, err := s.conn.ExistsW(prevNode)
		if err != nil {
			return "", err
		}

		if !exists {
			// 前一个节点已删除，重新检查锁状态
			children, _, err = s.conn.Children(path.Join(s.config.ZooKeeper.RootPath, "locks", lockPath))
			if err != nil {
				return "", err
			}
			if s.isLockAcquired(p, children) {
				return p, nil
			}
			continue
		}

		select {
		case <-ch:
			// 前一个节点发生变化，重新检查
			children, _, err = s.conn.Children(path.Join(s.config.ZooKeeper.RootPath, "locks", lockPath))
			if err != nil {
				return "", err
			}
			if s.isLockAcquired(p, children) {
				return p, nil
			}
		case <-ctx.Done():
			// 超时，释放节点
			s.conn.Delete(p, -1)
			return "", errors.New("acquire lock timeout")
		}
	}
}

// 释放锁
func (s *ZooKeeperService) ReleaseLock(lockPath string) error {
	return s.conn.Delete(lockPath, -1)
}

// 检查是否获得锁
func (s *ZooKeeperService) isLockAcquired(currentPath string, children []string) bool {
	if len(children) == 0 {
		return false
	}

	// 找到序号最小的节点
	minSeq := children[0]
	for _, child := range children {
		if child < minSeq {
			minSeq = child
		}
	}

	// 检查当前节点是否是最小序号节点
	return path.Base(currentPath) == minSeq
}

// 获取前一个节点
func (s *ZooKeeperService) getPreviousNode(currentPath string, children []string) string {
	currentSeq := path.Base(currentPath)

	var prevNode string
	for _, child := range children {
		if child < currentSeq && (prevNode == "" || child > prevNode) {
			prevNode = child
		}
	}

	if prevNode == "" {
		return ""
	}

	return path.Join(s.config.ZooKeeper.RootPath, "locks", path.Dir(currentPath), prevNode)
}

// 配置管理 - 设置配置
func (s *ZooKeeperService) SetConfig(key string, value interface{}) error {
	configPath := path.Join(s.config.ZooKeeper.RootPath, "config", key)

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	exists, _, err := s.conn.Exists(configPath)
	if err != nil {
		return err
	}

	if exists {
		_, err = s.conn.Set(configPath, data, -1)
	} else {
		_, err = s.conn.Create(configPath, data, 0, zk.WorldACL(zk.PermAll))
	}

	return err
}

// 配置管理 - 获取配置
func (s *ZooKeeperService) GetConfig(key string, target interface{}) error {
	configPath := path.Join(s.config.ZooKeeper.RootPath, "config", key)

	data, _, err := s.conn.Get(configPath)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, target)
}

// 配置管理 - 监听配置变化
func (s *ZooKeeperService) WatchConfig(key string, callback func([]byte)) error {
	configPath := path.Join(s.config.ZooKeeper.RootPath, "config", key)

	go s.watchNode(configPath, callback)
	return nil
}

// 监听节点变化
func (s *ZooKeeperService) watchNode(nodePath string, callback func([]byte)) {
	for {
		data, _, ch, err := s.conn.GetW(nodePath)
		if err != nil {
			log.Printf("Watch node %s error: %v", nodePath, err)
			time.Sleep(1 * time.Second)
			continue
		}

		// 首次触发回调
		callback(data)

		// 等待变化
		event := <-ch
		log.Printf("Node %s changed: %v", nodePath, event)
	}
}

// 服务注册
func (s *ZooKeeperService) RegisterService(serviceName, serviceAddr string) error {
	servicePath := path.Join(s.config.ZooKeeper.RootPath, "services", serviceName)

	// 创建临时节点，服务下线时自动删除
	_, err := s.conn.CreateProtectedEphemeralSequential(
		servicePath+"-",
		[]byte(serviceAddr),
		zk.WorldACL(zk.PermAll),
	)

	return err
}

// 服务发现
func (s *ZooKeeperService) DiscoverServices(serviceName string) ([]string, error) {
	servicePath := path.Join(s.config.ZooKeeper.RootPath, "services")

	children, _, err := s.conn.Children(servicePath)
	if err != nil {
		return nil, err
	}

	var services []string
	for _, child := range children {
		if len(child) >= len(serviceName) && child[:len(serviceName)] == serviceName {
			fullPath := path.Join(servicePath, child)
			data, _, err := s.conn.Get(fullPath)
			if err == nil {
				services = append(services, string(data))
			}
		}
	}

	return services, nil
}

// 关闭连接
func (s *ZooKeeperService) Close() {
	s.conn.Close()
}

func (s *ZooKeeperService) displayChildrenInfo() {
	productPath := s.config.ZooKeeper.RootPath

	fmt.Println("====== ZNode子节点结构 ======")

	// 获取直接子节点
	children, stat, err := s.conn.Children(productPath)
	if err != nil {
		log.Printf("获取子节点失败: %v", err)
		return
	}

	fmt.Printf("📁 节点路径: %s\n", productPath)
	fmt.Printf("子节点数量: %d\n", stat.NumChildren)
	fmt.Printf("直接子节点: %v\n", children)

	// 递归显示所有子节点
	s.displayTree(productPath, 0)
}

func (s *ZooKeeperService) displayTree(path string, level int) {
	children, _, err := s.conn.Children(path)
	if err != nil {
		return
	}
	sort.Strings(children)
	indent := ""
	for i := 0; i < level; i++ {
		indent += "  "
	}
	for _, child := range children {
		childPath := path + "/" + child
		data, stat, _ := s.conn.Get(childPath)

		nodeType := "📄"
		if stat.NumChildren > 0 {
			nodeType = "📁" // 目录图标
		}

		dataPreview := string(data)
		if len(dataPreview) > 30 {
			dataPreview = dataPreview[:30] + "..."
		}
		if len(dataPreview) == 0 {
			dataPreview = "(空)"
		}

		fmt.Printf("%s%s %s - 数据: %s\n", indent, nodeType, child, dataPreview)

		s.displayTree(childPath, level+1)
	}
}
