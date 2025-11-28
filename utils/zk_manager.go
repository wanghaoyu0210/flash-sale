package utils

import (
	"fmt"
	"log"
	"seckill-system/config"
	"seckill-system/services"
)

type ZooKeeperManager struct {
	zkService *services.ZooKeeperService
}

func NewZooKeeperManager(cfg *config.Config) (*ZooKeeperManager, error) {
	zkService, err := services.NewZooKeeperService(cfg)
	if err != nil {
		return nil, err
	}

	return &ZooKeeperManager{
		zkService: zkService,
	}, nil
}

// 更新库存配置
func (m *ZooKeeperManager) UpdateStockConfig(productID string, totalStock int, price int) error {
	stockConfig := map[string]interface{}{
		"product_id":  productID,
		"total_stock": totalStock,
		"price":       price,
	}

	return m.zkService.SetConfig("stock", stockConfig)
}

// 获取当前配置
func (m *ZooKeeperManager) GetStockConfig() (map[string]interface{}, error) {
	var config map[string]interface{}
	err := m.zkService.GetConfig("stock", &config)
	return config, err
}

// 列出所有注册的服务
func (m *ZooKeeperManager) ListServices() ([]string, error) {
	return m.zkService.DiscoverServices("seckill-service")
}

// 关闭连接
func (m *ZooKeeperManager) Close() {
	m.zkService.Close()
}

// 示例：动态更新库存
func ExampleZooKeeperUsage() {
	cfg := config.LoadConfig()
	zkManager, err := NewZooKeeperManager(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer zkManager.Close()

	// 更新库存
	err = zkManager.UpdateStockConfig("iphone15_pro_max_001", 2000, 5999)
	if err != nil {
		log.Fatal(err)
	}

	// 获取配置
	config, err := zkManager.GetStockConfig()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Current stock config: %+v\n", config)

	// 查看注册的服务
	services, err := zkManager.ListServices()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Registered services: %+v\n", services)
}
