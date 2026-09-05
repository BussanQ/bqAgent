package extagent

import "time"

// DetectionSnapshot never waits for background detection and never exposes commands.
type DetectionStatus struct {
	Agent     AgentName
	Available bool
	Transport string
	Fallback  bool
	Failed    bool
	CheckedAt time.Time
}

func (broker *Broker) DetectionSnapshot() ([]DetectionStatus, bool) {
	if broker == nil {
		return nil, false
	}
	select {
	case <-broker.detectionReady:
	default:
		return nil, false
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	result := make([]DetectionStatus, 0, len(broker.detections))
	for _, name := range SupportedAgents() {
		detection := broker.detections[name]
		status := DetectionStatus{Agent: name, Available: detection.Preferred != nil, Fallback: detection.CLIFallback, Failed: detection.StartupError != "", CheckedAt: broker.detectedAt}
		if detection.Preferred != nil {
			status.Transport = string(detection.Preferred.Kind)
		}
		result = append(result, status)
	}
	return result, true
}
