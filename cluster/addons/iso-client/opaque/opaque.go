package opaque

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/client/restclient"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
)

// struct which keeps all information necessary for advertising opaque resources
type OpaqueIntegerResourceAdvertiser struct {
	Node   string
	Name   string
	Value  string
	Config *restclient.Config
}

// body for opaque resource request
type opaqueIntegerResourceRequest struct {
	Operation string `json:"op"`
	Path      string `json:"path"`
	Value     string `json:"value"`
}

// constructor for OpaqueIntegerResourceAdvertiser
func NewOpaqueIntegerResourceAdvertiser(name string, value string, kubeConfigFile string) (*OpaqueIntegerResourceAdvertiser, error) {
	node, err := getNode()
	if err != nil {
		return nil, err
	}
	config, err := getClientConfig(kubeConfigFile)
	if err != nil {
		return nil, err
	}
	return &OpaqueIntegerResourceAdvertiser{
		Node:   node,
		Config: config,
		Name:   name,
		Value:  value,
	}, nil
}

func (opaque *OpaqueIntegerResourceAdvertiser) toRequestBody(operation string) ([]byte, error) {
	// body is in form of [{"op": "add", "path": "somepath","value": "somevalue"}]
	opaqueJson, err := json.Marshal([]opaqueIntegerResourceRequest{
		opaqueIntegerResourceRequest{
			Operation: operation,
			Path:      opaque.generateOpaqueResourcePath(),
			Value:     opaque.Value,
		},
	})
	if err != nil {
		return nil, err
	}
	return opaqueJson, nil
}

// TODO: check if kubelet is overriding hostname and return it instead
func getNode() (string, error) {
	return os.Hostname()
}

// path for opaque resources
func (opaque *OpaqueIntegerResourceAdvertiser) generateOpaqueResourcePath() string {
	return fmt.Sprintf("/status/capacity/pod.alpha.kubernetes.io~1opaque-int-resource-%s", opaque.Name)
}

// generate url for adding or removing opaque resources
func (opaque *OpaqueIntegerResourceAdvertiser) generateOpaqueResourceUrl() string {
	apiserver := opaque.Config.Host
	return fmt.Sprintf("%s/api/v1/nodes/%s/status", apiserver, opaque.Node)
}

// load configuration out of kubeconfig file which kubelet is using to connect to apiserver
func getClientConfig(kubeConfig string) (*restclient.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

// when using secure communication with apiServer retrieve certs from kubeconfig and use them
func (opaque *OpaqueIntegerResourceAdvertiser) createTlsTransport() (*http.Transport, error) {
	cert, err := tls.X509KeyPair(opaque.Config.TLSClientConfig.CertData, opaque.Config.TLSClientConfig.KeyData)
	if err != nil {

		return nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(opaque.Config.TLSClientConfig.CAData)
	tlsConfig := &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
	}

	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}
	return transport, nil
}

// prepare PATCH request for adding/removing opaque resources
func prepareRequest(body []byte, url string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json-patch+json")
	return req, nil
}

func (opaque *OpaqueIntegerResourceAdvertiser) createHttpClient() (*http.Client, error) {
	// TODO: test it against insecure apiserver
	if !opaque.Config.Insecure {
		transport, err := opaque.createTlsTransport()
		if err != nil {
			return nil, err
		}

		return &http.Client{Transport: transport}, nil
	}
	return &http.Client{}, nil
}

func (opaque *OpaqueIntegerResourceAdvertiser) advertiseOpaqueResource() error {
	return opaque.makeRequest("add")
}

func (opaque *OpaqueIntegerResourceAdvertiser) removeOpaqueResource() error {
	return opaque.makeRequest("remove")
}

func (opaque *OpaqueIntegerResourceAdvertiser) makeRequest(operation string) error {
	body, err := opaque.toRequestBody(operation)
	if err != nil {
		return err
	}

	req, err := prepareRequest(body, opaque.generateOpaqueResourceUrl())

	if err != nil {
		return err
	}

	client, err := opaque.createHttpClient()
	if err != nil {
		return err
	}

	glog.Infof("Advertising: %s, on such url: %v", string(body), opaque.generateOpaqueResourceUrl())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("Cannot set  opaque %s", opaque.Name)
	}

	return nil
}
