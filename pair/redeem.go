package pair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ShreyasBairyKS/Palate-Print-Agent/api"
)

// Redeem exchanges a single-use pairing code for a device credential and
// saves it. It returns the device id the server assigned.
func Redeem(ctx context.Context, client *http.Client, server, code, agentName string, store Store) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", errors.New("pairing code is empty")
	}
	body, _ := json.Marshal(api.PairRequest{Code: code, AgentName: agentName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+api.PathPair, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, api.MaxResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		var e api.Error
		json.Unmarshal(data, &e)
		return "", fmt.Errorf("pairing refused (%d %s): %s", resp.StatusCode, e.Code, e.Message)
	}
	var pr api.PairResponse
	if err := json.Unmarshal(data, &pr); err != nil || pr.Credential == "" {
		return "", fmt.Errorf("pairing: bad response from server")
	}
	if err := store.Save(pr.Credential); err != nil {
		return "", fmt.Errorf("pairing succeeded but the credential could not be saved: %w", err)
	}
	return pr.DeviceID, nil
}
