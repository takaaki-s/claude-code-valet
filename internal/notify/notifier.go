package notify

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Notifier sends desktop notifications
type Notifier struct {
	enabled bool
	// Debounce to avoid notification spam
	lastNotify map[string]time.Time
	mu         sync.Mutex
	// Minimum interval between notifications for the same session
	debounceInterval time.Duration
	// Remote notification settings
	remoteHost string
	remotePort string
}

// NewNotifier creates a new notifier
func NewNotifier() *Notifier {
	// Check for remote notification settings
	remoteHost := os.Getenv("LLM_MGR_NOTIFY_HOST")
	remotePort := os.Getenv("LLM_MGR_NOTIFY_PORT")
	if remotePort == "" {
		remotePort = "60000" // Default port
	}

	return &Notifier{
		enabled:          true,
		lastNotify:       make(map[string]time.Time),
		debounceInterval: 3 * time.Second,
		remoteHost:       remoteHost,
		remotePort:       remotePort,
	}
}

// SetEnabled enables or disables notifications
func (n *Notifier) SetEnabled(enabled bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.enabled = enabled
}

// NotifyPermission sends a notification when permission is required
func (n *Notifier) NotifyPermission(sessionID, sessionName string) {
	n.notify(sessionID, "Permission Required", fmt.Sprintf("[%s] Claude is waiting for permission", sessionName))
}

// NotifyTaskComplete sends a notification when a task is complete
func (n *Notifier) NotifyTaskComplete(sessionID, sessionName string) {
	n.notify(sessionID, "Task Complete", fmt.Sprintf("[%s] Claude has finished the task", sessionName))
}

// NotifyError sends a notification when an error occurs
func (n *Notifier) NotifyError(sessionID, sessionName string) {
	n.notify(sessionID, "Error", fmt.Sprintf("[%s] An error occurred", sessionName))
}

func (n *Notifier) notify(sessionID, title, message string) {
	n.mu.Lock()
	if !n.enabled {
		n.mu.Unlock()
		return
	}

	// Debounce: skip if we recently notified for this session
	key := sessionID + ":" + title
	if lastTime, ok := n.lastNotify[key]; ok {
		if time.Since(lastTime) < n.debounceInterval {
			n.mu.Unlock()
			return
		}
	}
	n.lastNotify[key] = time.Now()
	remoteHost := n.remoteHost
	remotePort := n.remotePort
	n.mu.Unlock()

	// Send notification asynchronously
	if remoteHost != "" {
		// Remote notification via TCP (for headless servers)
		go sendRemoteNotification(remoteHost, remotePort, title, message)
	} else {
		// Local desktop notification
		go sendDesktopNotification(title, message)
	}
}

// sendDesktopNotification sends a desktop notification using OS-specific methods
func sendDesktopNotification(title, message string) error {
	switch runtime.GOOS {
	case "darwin":
		return sendMacOSNotification(title, message)
	case "linux":
		return sendLinuxNotification(title, message)
	default:
		// Unsupported platform - silently ignore
		return nil
	}
}

// sendMacOSNotification sends a notification using osascript
func sendMacOSNotification(title, message string) error {
	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run()
}

// sendLinuxNotification sends a notification using notify-send
func sendLinuxNotification(title, message string) error {
	cmd := exec.Command("notify-send", title, message)
	return cmd.Run()
}

// sendRemoteNotification sends a notification to a remote host via TCP
// Compatible with notification-subscriber.sh format: {"title":"...", "message":"..."}
func sendRemoteNotification(host, port, title, message string) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Set write deadline
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	// Send JSON payload
	payload := map[string]string{
		"title":   title,
		"message": message,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Append newline for line-based reading
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}
