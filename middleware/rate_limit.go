package middleware

import (
	"net/http"
	"seckill-system/config"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type RateLimiter struct {
	redisClient *redis.Client
	config      *config.RateLimitConfig
	mu          sync.Mutex
	localLimit  map[string][]time.Time
}

func NewRateLimiter(redisClient *redis.Client, cfg *config.RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		redisClient: redisClient,
		config:      cfg,
		localLimit:  make(map[string][]time.Time),
	}
}

// 基于Redis的分布式限流
func (r *RateLimiter) RedisRateLimit(ctx *gin.Context) {
	clientIP := ctx.ClientIP()
	key := "rate_limit:" + clientIP

	current, err := r.redisClient.Get(ctx, key).Int()
	if err != nil && err != redis.Nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "系统繁忙"})
		ctx.Abort()
		return
	}

	if current >= r.config.Requests {
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
		ctx.Abort()
		return
	}

	// 增加计数
	pipe := r.redisClient.Pipeline()
	pipe.Incr(ctx, key)
	if current == 0 {
		// 第一次设置过期时间
		pipe.Expire(ctx, key, r.config.Duration)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "系统繁忙"})
		ctx.Abort()
		return
	}

	ctx.Next()
}

// 基于内存的本地限流
func (r *RateLimiter) LocalRateLimit(ctx *gin.Context) {
	clientIP := ctx.ClientIP()
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	// 清理过期的请求记录
	if requests, exists := r.localLimit[clientIP]; exists {
		var validRequests []time.Time
		for _, reqTime := range requests {
			if now.Sub(reqTime) <= r.config.Duration {
				validRequests = append(validRequests, reqTime)
			}
		}
		r.localLimit[clientIP] = validRequests
	}

	// 检查是否超过限制
	if len(r.localLimit[clientIP]) >= r.config.Requests {
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
		ctx.Abort()
		return
	}

	// 记录本次请求
	r.localLimit[clientIP] = append(r.localLimit[clientIP], now)
	ctx.Next()
}

// 滑动窗口限流中间件
func (r *RateLimiter) SlidingWindowRateLimit(ctx *gin.Context) {
	clientIP := ctx.ClientIP()
	now := time.Now()
	windowStart := now.Add(-r.config.Duration)

	r.mu.Lock()
	defer r.mu.Unlock()

	// 清理过期的请求记录
	if requests, exists := r.localLimit[clientIP]; exists {
		var validRequests []time.Time
		for _, reqTime := range requests {
			if reqTime.After(windowStart) {
				validRequests = append(validRequests, reqTime)
			}
		}
		r.localLimit[clientIP] = validRequests
	}

	// 检查是否超过限制
	if len(r.localLimit[clientIP]) >= r.config.Requests {
		ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
		ctx.Abort()
		return
	}

	// 记录本次请求
	r.localLimit[clientIP] = append(r.localLimit[clientIP], now)
	ctx.Next()
}

// Gin中间件 - 限流
func RateLimit(cfg config.RateLimitConfig) gin.HandlerFunc {
	// 可以根据需要选择Redis限流或本地限流
	limiter := NewRateLimiter(nil, &cfg)

	return func(ctx *gin.Context) {
		limiter.LocalRateLimit(ctx)
	}
}

// 令牌桶限流实现
type TokenBucket struct {
	capacity   int           // 桶容量
	tokens     int           // 当前令牌数量
	refillRate time.Duration // 填充速率
	lastRefill time.Time     // 最后填充时间
	mu         sync.Mutex
}

func NewTokenBucket(capacity int, refillRate time.Duration) *TokenBucket {
	return &TokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// 计算需要填充的令牌
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)
	tokensToAdd := int(elapsed / tb.refillRate)

	if tokensToAdd > 0 {
		tb.tokens = min(tb.capacity, tb.tokens+tokensToAdd)
		tb.lastRefill = now
	}

	if tb.tokens > 0 {
		tb.tokens--
		return true
	}

	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 令牌桶限流中间件
func TokenBucketRateLimit(capacity int, refillRate time.Duration) gin.HandlerFunc {
	tb := NewTokenBucket(capacity, refillRate)

	return func(ctx *gin.Context) {
		if !tb.Allow() {
			ctx.JSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后再试"})
			ctx.Abort()
			return
		}
		ctx.Next()
	}
}

// 多层次限流中间件
func MultiLevelRateLimit(limits map[string]config.RateLimitConfig) gin.HandlerFunc {
	limiters := make(map[string]*RateLimiter)

	for name, cfg := range limits {
		limiters[name] = NewRateLimiter(nil, &cfg)
	}

	return func(ctx *gin.Context) {
		// clientIP := ctx.ClientIP()
		// path := ctx.Request.URL.Path

		// IP级别限流
		if ipLimiter, exists := limiters["ip"]; exists {
			ipLimiter.mu.Lock()
			// 实现IP限流逻辑
			ipLimiter.mu.Unlock()
		}

		// 路径级别限流
		if pathLimiter, exists := limiters["path"]; exists {
			pathLimiter.mu.Lock()
			// 实现路径限流逻辑
			pathLimiter.mu.Unlock()
		}

		ctx.Next()
	}
}

// CORS中间件
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
