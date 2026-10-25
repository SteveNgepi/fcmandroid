package fcmandroid

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AppConfig describes the Android app the fake devices pretend to be. The values
// come from the app's google-services.json, except Cert.
type AppConfig struct {
	ProjectID string // Firebase project id, copied into Identity.Project
	AppID     string // Firebase app id, "1:NNN:android:..."
	SenderID  string // project number
	Package   string // Android package name
	Cert      string // SHA-1 of the app's signing certificate, hex, colons allowed
}

// NewAppConfig builds the config of one app from the contents of its
// google-services.json. The file may describe several apps: pkg picks one and
// may be empty only when the file has exactly one. cert is the SHA-1 of the
// app's signing certificate, which the file does not carry. The result is
// normalized and checked.
func NewAppConfig(googleServices []byte, pkg, cert string) (AppConfig, error) {
	var file googleServicesFile
	if err := json.Unmarshal(googleServices, &file); err != nil {
		return AppConfig{}, fmt.Errorf("fcmandroid: parse google-services.json: %w", err)
	}
	switch {
	case len(file.Client) == 0:
		return AppConfig{}, errors.New("fcmandroid: google-services.json has no client")
	case pkg == "" && len(file.Client) > 1:
		return AppConfig{}, fmt.Errorf("fcmandroid: google-services.json describes %d apps, pick one by package", len(file.Client))
	}
	for _, client := range file.Client {
		if pkg != "" && client.ClientInfo.AndroidInfo.Package != pkg {
			continue
		}
		if file.ProjectInfo.ProjectID == "" || file.ProjectInfo.ProjectNumber == "" ||
			client.ClientInfo.AppID == "" || client.ClientInfo.AndroidInfo.Package == "" {
			return AppConfig{}, errors.New("fcmandroid: google-services.json lacks project id, project number, app id or package")
		}
		cert = strings.ToLower(strings.ReplaceAll(cert, ":", "")) // the form register3 takes
		if !sha1Hex.MatchString(cert) {
			return AppConfig{}, errors.New("fcmandroid: cert must be a SHA-1 fingerprint (40 hex digits)")
		}
		return AppConfig{
			ProjectID: file.ProjectInfo.ProjectID,
			AppID:     client.ClientInfo.AppID,
			SenderID:  file.ProjectInfo.ProjectNumber,
			Package:   client.ClientInfo.AndroidInfo.Package,
			Cert:      cert,
		}, nil
	}
	return AppConfig{}, fmt.Errorf("fcmandroid: google-services.json has no app with package %q", pkg)
}

var sha1Hex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// googleServicesFile is the part of google-services.json that AppConfig reads.
type googleServicesFile struct {
	ProjectInfo struct {
		ProjectID     string `json:"project_id"`
		ProjectNumber string `json:"project_number"`
	} `json:"project_info"`
	Client []struct {
		ClientInfo struct {
			AppID       string `json:"mobilesdk_app_id"`
			AndroidInfo struct {
				Package string `json:"package_name"`
			} `json:"android_client_info"`
		} `json:"client_info"`
	} `json:"client"`
}
