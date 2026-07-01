/*
Copyright © 2020 Anton Kramarev
Copyright © 2024 Perfana Software B.V.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

type LogLevel int

// Log levels ordered by increasing verbosity: ERROR is the least verbose
// (always emitted) and DEBUG is the most verbose. A message is emitted when
// the configured logLevel is greater than or equal to the message level.
const (
	ERROR LogLevel = iota
	INFO
	DEBUG
)

// Dedicated loggers per level. Each has a fixed output and prefix, so no
// shared mutable state is touched at log time. This makes concurrent logging
// from multiple goroutines safe: log.Logger.Output is internally synchronized,
// and we no longer mutate output/prefix on a shared logger between calls.
var (
	infoLogger  *log.Logger
	errorLogger *log.Logger
	debugLogger *log.Logger
	logLevel    LogLevel = INFO // Default to INFO level
)

// InitLogger sets up new logger instances that write to file and STDOUT/STDERR
func InitLogger(fileName string) error {
	p, err := filepath.Abs(fileName)
	if err != nil {
		return fmt.Errorf("failed to build absolute path for log file: %w", err)
	}
	dir := filepath.Dir(p)
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err != nil {
		return fmt.Errorf("cannot create log file at %s: %w", fileName, err)
	}
	sw := io.MultiWriter(os.Stdout, file)
	ew := io.MultiWriter(os.Stderr, file)
	flags := log.Ldate | log.Ltime | log.LUTC
	infoLogger = log.New(sw, "INFO ", flags)
	errorLogger = log.New(ew, "ERROR ", flags)
	debugLogger = log.New(sw, "DEBUG ", flags)

	return nil
}

func SetLogLevel(level LogLevel) {
	logLevel = level
}

func Errorln(v ...interface{}) {
	if logLevel >= ERROR {
		errorLogger.Println(v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if logLevel >= ERROR {
		errorLogger.Printf(format, v...)
	}
}
func Infoln(v ...interface{}) {
	if logLevel >= INFO {
		infoLogger.Println(v...)
	}
}

func Infof(format string, v ...interface{}) {
	if logLevel >= INFO {
		infoLogger.Printf(format, v...)
	}
}

func Debugln(v ...interface{}) {
	if logLevel >= DEBUG {
		debugLogger.Println(v...)
	}
}

func Debugf(format string, v ...interface{}) {
	if logLevel >= DEBUG {
		debugLogger.Printf(format, v...)
	}
}
