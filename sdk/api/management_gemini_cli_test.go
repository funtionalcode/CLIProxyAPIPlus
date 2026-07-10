package api

import "github.com/gin-gonic/gin"

type geminiCLITokenRequester interface {
	RequestGeminiCLIToken(*gin.Context)
}

var _ geminiCLITokenRequester = (ManagementTokenRequester)(nil)
var _ geminiCLITokenRequester = (*managementTokenRequester)(nil)
