package main

import (
	"flag"
	"fmt"
	"github/ruoyiran/claude-code-transformer/src/config"
	"github/ruoyiran/claude-code-transformer/src/formatter"
	"github/ruoyiran/claude-code-transformer/src/router"
	"log"
	"path"
	"runtime"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

var (
	confPath string
)

func init() {
	flag.StringVar(&confPath, "conf", "./conf/config.yaml", "config path")
}

func initLogger(logConf config.LogConfig) error {
	logrus.SetReportCaller(true)
	logrus.SetFormatter(&formatter.EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%] %time% - %src% %msg%\n",
		CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
			fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
			return "", fileName
		},
	})

	multiWriter, err := config.NewLogWriter(logConf)
	if err != nil {
		return err
	}
	logrus.SetOutput(multiWriter)
	if logConf.Level == "debug" {
		logrus.SetLevel(logrus.DebugLevel)
	} else if logConf.Level == "info" {
		logrus.SetLevel(logrus.InfoLevel)
	} else if logConf.Level == "warn" {
		logrus.SetLevel(logrus.WarnLevel)
	} else if logConf.Level == "error" {
		logrus.SetLevel(logrus.ErrorLevel)
	} else if logConf.Level == "fatal" {
		logrus.SetLevel(logrus.FatalLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
	gin.DefaultWriter = multiWriter
	gin.DefaultErrorWriter = multiWriter
	return nil
}

func main() {
	flag.Parse()
	gin.SetMode(gin.ReleaseMode)

	var conf *config.Config
	var err error
	conf, err = config.LoadConfigFromPath(confPath)
	if err != nil {
		log.Fatalf("failed to load config from path: %s, error: %s", confPath, err.Error())
		return
	}
	if conf == nil {
		log.Fatalf("failed to load config from path: %s, conf is nil", confPath)
		return
	}
	if err := initLogger(conf.Log); err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}

	r := router.CreateEngine()

	for _, rt := range r.Routes() {
		logrus.Infof("%-6s %-30s -> %s", rt.Method, rt.Path, rt.Handler)
	}

	addr := fmt.Sprintf("%s:%d", config.GetConfig().ServerAddr, config.GetConfig().ServerPort)
	logrus.Infof("Server listen on: %s.\n", addr)
	err = r.Run(addr)
	if err != nil {
		logrus.Fatalln(err)
	}
}
