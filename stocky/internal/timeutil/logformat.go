package timeutil

import "github.com/sirupsen/logrus"

// LogFormatter wraps another logrus formatter and converts each entry's
// timestamp to IST first, so log times never depend on the machine's TZ.
type LogFormatter struct {
	Inner logrus.Formatter
}

// Format implements logrus.Formatter.
func (f *LogFormatter) Format(e *logrus.Entry) ([]byte, error) {
	// logrus builds a fresh Entry for every log call, so changing it is safe.
	e.Time = e.Time.In(IST)
	return f.Inner.Format(e)
}
