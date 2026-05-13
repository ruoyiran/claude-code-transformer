package config

import (
	"github/ruoyiran/claude-code-transformer/src/formatter"
	"io"
	"os"
	"path"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

type LogConfig struct {
	FileName   string `yaml:"filename"`
	Level      string `yaml:"level"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
	Compress   bool   `yaml:"compress"`
}

type dailyRotateWriter struct {
	mu         sync.Mutex
	logger     *lumberjack.Logger
	now        func() time.Time
	currentDay string
}

func NewLogWriter(logConf LogConfig) (io.Writer, error) {
	fileWriter, err := newDailyRotateWriter(logConf)
	if err != nil {
		return nil, err
	}
	return io.MultiWriter(os.Stdout, fileWriter), nil
}

func newDailyRotateWriter(logConf LogConfig) (*dailyRotateWriter, error) {
	logger := &lumberjack.Logger{
		Filename:  logConf.FileName,
		MaxSize:   logConf.MaxSize,
		MaxAge:    logConf.MaxAge,
		Compress:  logConf.Compress,
		LocalTime: true,
	}

	writer := &dailyRotateWriter{
		logger: logger,
		now:    time.Now,
	}

	currentDay, err := currentLogDay(logger.Filename, writer.now())
	if err != nil {
		return nil, err
	}
	writer.currentDay = currentDay
	return writer, nil
}

func currentLogDay(filename string, now time.Time) (string, error) {
	if filename != "" {
		info, err := os.Stat(filename)
		if err == nil {
			return logDayKey(info.ModTime()), nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return logDayKey(now), nil
}

func logDayKey(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

func (w *dailyRotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	day := logDayKey(now)
	if day != w.currentDay {
		if err := w.logger.Rotate(); err != nil {
			return 0, err
		}
		w.currentDay = day
	}

	return w.logger.Write(p)
}

func (w *dailyRotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.logger.Close()
}

func InitLoggerWithDefaultConfig() {
	logrus.SetReportCaller(true)
	logrus.SetFormatter(&formatter.EasyFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		LogFormat:       "[%lvl%]: %time% - %src% %msg%\n",
		CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
			fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
			return "", fileName
		},
	})

	output, err := NewLogWriter(LogConfig{
		FileName:   "log.txt",
		MaxSize:    100,
		MaxBackups: 20,
		MaxAge:     30,
		Compress:   true,
	})
	if err != nil {
		output = os.Stdout
	}
	logrus.SetOutput(output)
	logrus.SetLevel(logrus.DebugLevel)
}
