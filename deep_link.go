package subspacerelay

import (
	"encoding/hex"
	"net/url"
	"strings"
)

const (
	ModeCard          = "card"
	ModeReader        = "reader"
	ModeReaderDynamic = "reader-dynamic"
)

func DeepLink(brokerURL string, mode string, discoveryPublicKey []byte) (string, error) {
	var u *url.URL
	var err error
	if brokerURL != "" {
		u, err = url.Parse(brokerURL)
		if err != nil {
			return "", err
		}
	} else {
		u = &url.URL{}
	}

	var isWebsocket bool
	var isPlaintext bool
	switch u.Scheme {
	case "mqtt":
		isPlaintext = true
	case "ws":
		isWebsocket = true
		isPlaintext = true
	case "wss":
		isWebsocket = true
	}

	params := make(url.Values)
	if isWebsocket {
		params.Add("websocket", "true")
	}
	if isPlaintext {
		params.Add("tls", "false")
	}
	if u.Path != "" {
		path := u.Path
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		params.Add("path", u.Path)
	}
	if discoveryPublicKey != nil {
		params.Add("discovery", strings.ToUpper(hex.EncodeToString(discoveryPublicKey)))
	}

	u.RawQuery = params.Encode()
	if mode != "" {
		u.Path = "/" + mode
	} else {
		u.Path = ""
	}
	u.Scheme = "subspace-relay"

	return u.String(), nil
}
