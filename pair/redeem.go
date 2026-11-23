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
		return "", &RefusedError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
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

// RefusedError is the server answering a pairing request with an error: a
// wrong or expired code (401 invalid_code), too many tries (429), a wrong
// address (404), and so on. Status and Code let the setup window explain it
// in plain words while still showing the code.
type RefusedError struct {
	Status  int    // the HTTP status
	Code    string // the server's error code, such as "invalid_code"; may be empty
	Message string // the server's message; may be empty
}

func (e *RefusedError) Error() string {
	if e.Status == http.StatusNotFound {
		return fmt.Sprintf("pairing: the server has nothing at %s (404). %s", api.PathPair, api.AddressHint)
	}
	return fmt.Sprintf("pairing refused (%d %s): %s", e.Status, e.Code, e.Message)
}
