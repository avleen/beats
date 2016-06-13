package syslog

import (
	"errors"
	"expvar"
	"fmt"
	"os"
	"time"

	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/outputs/transport"
)

// Metrics that can retrieved through the expvar web interface.
var (
	shippedLines = expvar.NewInt("libbeatSyslogShippedLines")
)

type client struct {
	*transport.Client
	SyslogProgram  string
	SyslogPriority uint64
	SyslogSeverity uint64
	Hostname       string
}

func newClient(tc *transport.Client, prog string, pri uint64, sev uint64) *client {
	// hostname only needs to be set once.
	// It's already set in the event by the publisher, but it doesn't make
	// sense to waste CPU extracting it from there for each event, when it's
	// always going to be the same. So let's set it once here and reuse.
	hostname, err := os.Hostname()
	if err != nil {
		logp.Err("Count not get hostname: %v. Setting to 'unknown'.", err)
		hostname = "unknown"
	}

	return &client{
		Client:         tc,
		SyslogProgram:  prog,
		SyslogPriority: pri,
		SyslogSeverity: sev,
		Hostname:       hostname,
	}
}

// errors
var (
	ErrNotConnected = errors.New("Syslog client is not connected")
)

func (c *client) Connect(timeout time.Duration) error {
	logp.Debug("syslog", "connect")
	return c.Client.Connect()
}

func (c *client) Close() error {
	logp.Debug("syslog", "close connection")
	return c.Client.Close()
}

func (c *client) PublishEvent(event common.MapStr) error {
	_, err := c.PublishEvents([]common.MapStr{event})
	return err
}

func (c *client) CreateSyslogString(event common.MapStr) (string, error) {
	// Pull some values from the event, which we'll use to construct
	// our syslog string.

	// @timestamp is guaranteed to be present.
	// We need it in RFC3339 format for syslog
	ts := time.Time(event["@timestamp"].(common.Time)).UTC().Format(time.RFC3339)

	var local_prog string = c.SyslogProgram
	var local_pri uint64 = c.SyslogPriority
	var local_sev uint64 = c.SyslogSeverity

	// Check for overrides from the event, if event["fields"] exists
	if _, ok := event["fields"]; ok {
		// A value for program may have been supplied in the config.
		if program_name, ok := event["fields"].(common.MapStr)["program"]; ok {
			local_prog = program_name.(string)
		}

		// A value for priority may have been supplied in the config.
		if priority_num, ok := event["fields"].(common.MapStr)["priority"]; ok {
			// If "priority" in the config is 0 (for kernel
			// messages), we get the following panic:
			//  panic: interface conversion: interface is int64, not uint64
			// If we change the code to assert int64 rather than
			// uint64, the panic becomes:
			//  panic: interface conversion: interface is uint64, not int64
			// This is ridiculous, so we'll just assume the source
			// is int64, and convert to unit64. If that fails, the
			// source number was probably out of bounds anyway for
			// valid syslog priorities.
			local_pri = uint64(priority_num.(int64))
		}

		// A value for severity may have been supplied in the config.
		if severity_num, ok := event["fields"].(common.MapStr)["severity"]; ok {
			// See note above, on why we assert int64 and then
			// convert to unit64
			local_sev = uint64(severity_num.(int64))
		}
	}

	// Calculate the PRI number for the protocol according to RFC5424:
	// If the priority and severity are both zero, use "0".
	// If the priority is zero but the severity is not, print a
	// leading zero followed by the severity.
	// Otherwise, multiple the priority by 8, and add the severity.
	var pri_num string
	if local_pri == 0 && local_sev == 0 {
		pri_num = "0"
	} else if local_pri == 0 && local_sev != 0 {
		pri_num = fmt.Sprintf("0%d", local_sev)
	} else {
		pri_num = fmt.Sprintf("%d", ((local_pri * 8) + local_sev))
	}

	// This is the log line which was read in.
	msg := *event["message"].(*string)
	line := fmt.Sprintf("<%s>%s %s %s: %s\n", pri_num, ts, c.Hostname,
		local_prog, msg)
	return line, nil

}

// PublishEvents sends all events to syslog. On error a slice with all events
// not published will be returned.
func (c *client) PublishEvents(events []common.MapStr) ([]common.MapStr, error) {
	for _, event := range events {
		msg, _ := c.CreateSyslogString(event)
		// Send the message down the wire.
		c.Write([]byte(msg))
		shippedLines.Add(1)
	}
	return events, nil
}
