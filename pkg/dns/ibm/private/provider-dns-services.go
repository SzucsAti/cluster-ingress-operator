package private

import (
	"fmt"
	"time"

	"github.com/IBM/go-sdk-core/v4/core"
	dnssvcsv1 "github.com/IBM/networking-go-sdk/dnssvcsv1"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	iov1 "github.com/openshift/api/operatoringress/v1"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")
)

type Provider struct {
	dnsServices map[string]*dnssvcsv1.DnsSvcsV1
	config      Config
}

type Config struct {
	APIKey     string
	InstanceID string
	UserAgent  string
	Zones      []string
}

func NewProvider(config Config) (*Provider, error) {
	if len(config.Zones) < 1 {
		return nil, fmt.Errorf("missing zone data")
	}

	provider := &Provider{}
	provider.dnsServices = make(map[string]*dnssvcsv1.DnsSvcsV1)

	authenticator := &core.IamAuthenticator{
		ApiKey: config.APIKey,
	}
	for _, zone := range config.Zones {
		options := &dnssvcsv1.DnsSvcsV1Options{
			URL:           "https://api.dns-svcs.cloud.ibm.com",
			Authenticator: authenticator,
		}

		dnsService, err := dnssvcsv1.NewDnsSvcsV1(options)
		if err != nil {
			return nil, fmt.Errorf("failed to create a new DNS Service instance: %w", err)
		}
		dnsService.EnableRetries(3, 5*time.Second)
		dnsService.Service.SetUserAgent(config.UserAgent)

		provider.dnsServices[zone] = dnsService

		log.Info("check zones", "zone", zone)

		provider.config.InstanceID = config.InstanceID
	}

	if err := validateDNSServices(provider); err != nil {
		return nil, fmt.Errorf("failed to validate ibm dns services: %w", err)
	}

	log.Info("Successfully validated DNSServices")

	return provider, nil
}

func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return nil
}

func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return nil
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return nil
}

// validateDNSServices validates that provider clients can communicate with
// associated API endpoints by having each client make a get DNS records call.
func validateDNSServices(provider *Provider) error {
	var errs []error
	maxItems := int64(1)
	for zone, dnsService := range provider.dnsServices {
		opt := dnsService.NewListResourceRecordsOptions(provider.config.InstanceID, zone)
		opt.SetLimit(maxItems)
		if _, _, err := dnsService.ListResourceRecords(opt); err != nil {
			errs = append(errs, fmt.Errorf("failed to get dns records: %v", err))
		}
	}
	return kerrors.NewAggregate(errs)
}
