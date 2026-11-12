package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DownloadArtifact uses the same node mTLS identity. B04's resolver supplies the
// pinned key; B05 receives verified bytes and decides whether to stage them.
func (c *Client) DownloadArtifact(ctx context.Context, key protocol.ArtifactKey, maximum int64) ([]byte, error) {
	if key.NodeID != c.o.NodeID || !key.TaskID.Valid() || !key.TargetRevision.Valid() || len(key.TargetDigest) != 64 || maximum < 1 || maximum > 16<<20 {
		return nil, ErrUnavailable
	}
	config, e := c.o.TLSConfig()
	if e != nil || config == nil || config.InsecureSkipVerify || config.RootCAs == nil || config.ServerName == "" || len(config.Certificates) == 0 {
		return nil, ErrUnavailable
	}
	u, e := url.Parse(c.o.URL)
	if e != nil {
		return nil, ErrUnavailable
	}
	u.Scheme = "https"
	u.Path = "/control/v1/tasks/" + string(key.TaskID) + "/bundle"
	transport := &http.Transport{TLSClientConfig: config.Clone(), TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, ErrUnavailable
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	if e != nil || int64(len(data)) > maximum {
		clear(data)
		return nil, ErrUnavailable
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != key.TargetDigest {
		clear(data)
		return nil, ErrUnavailable
	}
	return data, nil
}
