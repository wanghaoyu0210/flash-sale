package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"seckill-system/config"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

type SeckillService struct {
	redisClient *redis.Client
	rabbitConn  *amqp.Connection
	zkService   *ZooKeeperService
	config      *config.Config
}

type SeckillRequest struct {
	UserID    string `json:"user_id"`
	ProductID string `json:"product_id"`
	Timestamp int64  `json:"timestamp"`
}

type SeckillResult struct {
	Success bool   `json:"success"`
	OrderID string `json:"order_id,omitempty"`
	Message string `json:"message,omitempty"`
}

func NewSeckillService(redisClient *redis.Client, rabbitConn *amqp.Connection, zkService *ZooKeeperService, cfg *config.Config) *SeckillService {
	service := &SeckillService{
		redisClient: redisClient,
		rabbitConn:  rabbitConn,
		zkService:   zkService,
		config:      cfg,
	}

	// 初始化ZooKeeper配置
	service.initZooKeeperConfig()
	// 启动配置监听
	service.startConfigWatch()

	return service
}

// 初始化ZooKeeper配置
func (s *SeckillService) initZooKeeperConfig() {
	// 设置初始库存到ZooKeeper
	stockConfig := map[string]interface{}{
		"total_stock": s.config.Seckill.TotalStock,
		"product_id":  s.config.Seckill.ProductID,
		"price":       s.config.Seckill.SeckillPrice,
	}

	if err := s.zkService.SetConfig("stock", stockConfig); err != nil {
		log.Printf("Failed to set stock config in ZooKeeper: %v", err)
	}
}

// 启动配置监听
func (s *SeckillService) startConfigWatch() {
	// 监听库存配置变化
	s.zkService.WatchConfig("stock", func(data []byte) {
		var stockConfig map[string]interface{}
		if err := json.Unmarshal(data, &stockConfig); err == nil {
			log.Printf("Stock config updated: %v", stockConfig)
			// 这里可以更新本地配置或执行其他逻辑
		}
	})
}

// 使用ZooKeeper分布式锁的秒杀处理
func (s *SeckillService) ProcessSeckillWithZKLock(ctx context.Context, userID, productID string) (*SeckillResult, error) {
	// 使用ZooKeeper分布式锁保护关键操作
	lockPath := "seckill_" + productID
	lock, err := s.zkService.AcquireLock(lockPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer s.zkService.ReleaseLock(lock)

	// 在锁内执行核心秒杀逻辑
	return s.processSeckillCore(ctx, userID, productID)
}

// 核心秒杀逻辑（被分布式锁保护）
func (s *SeckillService) processSeckillCore(ctx context.Context, userID, productID string) (*SeckillResult, error) {
	// 1. 检查用户是否已经参与过秒杀
	userKey := "user_seckill:" + productID + ":" + userID
	exists, err := s.redisClient.Exists(ctx, userKey).Result()
	if err != nil {
		return nil, err
	}
	if exists > 0 {
		return &SeckillResult{
			Success: false,
			Message: "您已经参与过本次秒杀",
		}, nil
	}

	// 2. 检查库存
	stockKey := "seckill_stock:" + productID
	stock, err := s.redisClient.Get(ctx, stockKey).Int()
	if err != nil {
		return nil, err
	}

	if stock <= 0 {
		return &SeckillResult{
			Success: false,
			Message: "商品已售罄",
		}, nil
	}

	// 3. 使用Lua脚本保证原子性减库存
	luaScript := `
		local stock_key = KEYS[1]
		local user_key = KEYS[2]
		local stock = tonumber(redis.call('GET', stock_key) or 0)
		
		if stock <= 0 then
			return 0
		end
		
		if redis.call('EXISTS', user_key) == 1 then
			return -1
		end
		
		redis.call('DECR', stock_key)
		redis.call('SET', user_key, 1, 'EX', 3600)
		return 1
	`

	result, err := s.redisClient.Eval(ctx, luaScript, []string{stockKey, userKey}).Int()
	if err != nil {
		return nil, err
	}

	switch result {
	case 0:
		return &SeckillResult{
			Success: false,
			Message: "商品已售罄",
		}, nil
	case -1:
		return &SeckillResult{
			Success: false,
			Message: "您已经参与过本次秒杀",
		}, nil
	}

	// 4. 生成订单ID
	orderID := generateOrderID(userID, productID)

	// 5. 发送消息到RabbitMQ进行异步处理
	seckillReq := SeckillRequest{
		UserID:    userID,
		ProductID: productID,
		Timestamp: time.Now().Unix(),
	}

	if err := s.sendToQueue(ctx, seckillReq, orderID); err != nil {
		// 如果消息发送失败，回滚库存
		s.redisClient.Incr(ctx, stockKey)
		s.redisClient.Del(ctx, userKey)
		return nil, err
	}

	return &SeckillResult{
		Success: true,
		OrderID: orderID,
		Message: "秒杀成功，正在生成订单",
	}, nil
}

// 获取商品信息
func (s *SeckillService) GetProductInfo(ctx context.Context, productID string) (map[string]interface{}, error) {
	// 从Redis缓存获取商品信息
	cacheKey := "product:" + productID
	productInfo, err := s.redisClient.HGetAll(ctx, cacheKey).Result()

	if err == redis.Nil || len(productInfo) == 0 {
		// 缓存未命中，从数据库获取（这里简化为硬编码）
		product := map[string]interface{}{
			"id":            productID,
			"name":          "iPhone 15 Pro Max",
			"price":         9999,
			"seckill_price": s.config.Seckill.SeckillPrice,
			"description":   "最新款苹果手机",
			"image":         "/static/images/iphone.jpg",
			"stock":         s.getStock(ctx, productID),
		}

		// 存入Redis缓存
		s.redisClient.HSet(ctx, cacheKey, product)
		s.redisClient.Expire(ctx, cacheKey, 30*time.Minute)

		return product, nil
	}

	// 添加实时库存信息
	stock := s.getStock(ctx, productID)
	productInfo["stock"] = strconv.Itoa(stock)

	return convertStringMapToInterface(productInfo), nil
}

// 将 map[string]string 转换为 map[string]interface{}
func convertStringMapToInterface(stringMap map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for key, value := range stringMap {
		result[key] = value
	}
	return result
}

// 秒杀核心逻辑
func (s *SeckillService) ProcessSeckill(ctx context.Context, userID, productID string) (*SeckillResult, error) {
	// 根据配置选择是否使用ZooKeeper分布式锁
	// 在实际生产环境中，可以根据压力情况动态选择
	useZKLock := true

	if useZKLock {
		return s.ProcessSeckillWithZKLock(ctx, userID, productID)
	} else {
		// 使用原有的Redis Lua脚本方式
		return s.processSeckillCore(ctx, userID, productID)
	}
}

// 发送消息到RabbitMQ
func (s *SeckillService) sendToQueue(ctx context.Context, req SeckillRequest, orderID string) error {
	ch, err := s.rabbitConn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	queue, err := ch.QueueDeclare(
		"seckill_orders", // 队列名
		true,             // 持久化
		false,            // 自动删除
		false,            // 排他性
		false,            // 不等待
		nil,              // 参数
	)
	if err != nil {
		return err
	}

	message := map[string]interface{}{
		"order_id":   orderID,
		"user_id":    req.UserID,
		"product_id": req.ProductID,
		"timestamp":  req.Timestamp,
	}

	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	err = ch.PublishWithContext(ctx,
		"",         // 交换机
		queue.Name, // 路由键
		false,      // 强制
		false,      // 立即
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/json",
			Body:         body,
		})

	return err
}

// 获取库存
func (s *SeckillService) getStock(ctx context.Context, productID string) int {
	stockKey := "seckill_stock:" + productID
	stock, err := s.redisClient.Get(ctx, stockKey).Int()
	if err != nil {
		// 如果Redis中没有库存数据，初始化库存
		if errors.Is(err, redis.Nil) {
			s.redisClient.Set(ctx, stockKey, s.config.Seckill.TotalStock, 0)
			return s.config.Seckill.TotalStock
		}
		return 0
	}
	return stock
}

func generateOrderID(userID, productID string) string {
	timestamp := time.Now().UnixNano()
	return userID + "_" + productID + "_" + strconv.FormatInt(timestamp, 10)
}
