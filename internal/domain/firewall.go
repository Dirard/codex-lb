package domain

import "time"

type FirewallEntry struct {
	IPAddress string    `json:"ipAddress"`
	CreatedAt time.Time `json:"createdAt"`
}
