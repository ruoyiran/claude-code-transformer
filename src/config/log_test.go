package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const lumberjackBackupTimeFormat = "2006-01-02T15-04-05.000"

func TestDailyRotateWriter_RotatesWhenDayChanges(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "server.log")
	require.NoError(t, os.WriteFile(logPath, []byte("day-1\n"), 0o644))

	day1 := time.Date(2026, time.May, 13, 23, 30, 0, 0, time.Local)
	day2 := day1.Add(2 * time.Hour)
	require.NoError(t, os.Chtimes(logPath, day1, day1))

	writer, err := newDailyRotateWriter(LogConfig{FileName: logPath})
	require.NoError(t, err)
	writer.now = func() time.Time { return day2 }

	n, err := writer.Write([]byte("day-2\n"))
	require.NoError(t, err)
	require.Equal(t, len("day-2\n"), n)
	require.NoError(t, writer.Close())

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, "day-2\n", string(data))

	matches, err := filepath.Glob(strings.TrimSuffix(logPath, ".log") + "-*.log")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	backupData, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, "day-1\n", string(backupData))
}

func TestDailyRotateWriter_RemovesBackupsOlderThanMaxAge(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "server.log")

	oldBackup := lumberjackBackupName(logPath, time.Now().Add(-72*time.Hour))
	recentBackup := lumberjackBackupName(logPath, time.Now().Add(-2*time.Hour))
	require.NoError(t, os.WriteFile(oldBackup, []byte("old\n"), 0o644))
	require.NoError(t, os.WriteFile(recentBackup, []byte("recent\n"), 0o644))

	writer, err := newDailyRotateWriter(LogConfig{
		FileName: logPath,
		MaxAge:   1,
	})
	require.NoError(t, err)

	_, err = writer.Write([]byte("current\n"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := os.Stat(oldBackup)
		return os.IsNotExist(err)
	}, 2*time.Second, 20*time.Millisecond)

	_, err = os.Stat(recentBackup)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}

func lumberjackBackupName(filename string, ts time.Time) string {
	ext := filepath.Ext(filename)
	prefix := strings.TrimSuffix(filepath.Base(filename), ext)
	name := prefix + "-" + ts.Format(lumberjackBackupTimeFormat) + ext
	return filepath.Join(filepath.Dir(filename), name)
}
