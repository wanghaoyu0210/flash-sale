package main

import (
	"log"
	"seckill-system/config"
	"seckill-system/controllers"
	"seckill-system/middleware"
	"seckill-system/services"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 加载配置
	cfg := config.LoadConfig()

	// 初始化Redis
	redisClient := initRedis(cfg)

	// 初始化RabbitMQ
	rabbitConn, err := amqp.Dial(cfg.RabbitMQ.URL)
	if err != nil {
		log.Fatalf("Failed to connect to RabbitMQ: %v", err)
	}
	defer rabbitConn.Close()

	// 初始化ZooKeeper
	zkService, err := services.NewZooKeeperService(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to ZooKeeper: %v", err)
	}
	defer zkService.Close()

	// 注册当前服务实例
	if err := zkService.RegisterService("seckill-service", "localhost:"+cfg.Server.Port); err != nil {
		log.Printf("Failed to register service: %v", err)
	}

	// 初始化服务层
	seckillService := services.NewSeckillService(redisClient, rabbitConn, zkService, cfg)

	// 初始化控制器
	seckillController := controllers.NewSeckillController(seckillService)

	// 设置Gin路由
	r := setupRouter(seckillController, cfg)

	// 启动服务
	log.Printf("Server starting on port %s", cfg.Server.Port)
	r.Run(":" + cfg.Server.Port)
}

func initRedis(cfg *config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
}

func setupRouter(controller *controllers.SeckillController, cfg *config.Config) *gin.Engine {
	r := gin.Default()

	// 全局中间件
	r.Use(middleware.RateLimit(cfg.RateLimit)) // IP限流
	r.Use(middleware.CORS())

	// 静态文件服务
	r.Static("/static", "./static")

	// API路由
	api := r.Group("/api/v1")
	{
		api.GET("/product/:id", controller.GetProductInfo)
		api.POST("/seckill/:id", controller.Seckill)
		api.GET("/seckill/result/:orderId", controller.GetSeckillResult)
	}

	return r
}
