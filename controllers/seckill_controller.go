package controllers

import (
	"net/http"
	"seckill-system/services"

	"github.com/gin-gonic/gin"
)

type SeckillController struct {
	seckillService *services.SeckillService
}

func NewSeckillController(service *services.SeckillService) *SeckillController {
	return &SeckillController{
		seckillService: service,
	}
}

// 获取商品信息
func (c *SeckillController) GetProductInfo(ctx *gin.Context) {
	productID := ctx.Param("id")

	productInfo, err := c.seckillService.GetProductInfo(ctx, productID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"error": "获取商品信息失败",
		})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    productInfo,
	})
}

// 秒杀接口
func (c *SeckillController) Seckill(ctx *gin.Context) {
	productID := ctx.Param("id")

	// 从Header或Token中获取用户ID（这里简化为从Header获取）
	userID := ctx.GetHeader("X-User-ID")
	if userID == "" {
		ctx.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "用户未登录",
		})
		return
	}

	result, err := c.seckillService.ProcessSeckill(ctx, userID, productID)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "系统繁忙，请重试",
		})
		return
	}

	ctx.JSON(http.StatusOK, result)
}

// 获取秒杀结果
func (c *SeckillController) GetSeckillResult(ctx *gin.Context) {
	orderID := ctx.Param("orderId")

	// 这里应该查询订单状态，简化处理
	ctx.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"order_id": orderID,
			"status":   "processing", // processing, success, failed
		},
	})
}
