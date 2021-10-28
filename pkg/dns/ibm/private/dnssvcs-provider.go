package private

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	dnssvcsv1 "github.com/IBM/networking-go-sdk/dnssvcsv1"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/private/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	iov1 "github.com/openshift/api/operatoringress/v1"
)

var (
	_                       dns.Provider = &Provider{}
	log                                  = logf.Logger.WithName("dns")
	defaultDNSSVCSRecordTTL              = int64(120)
)

type Provider struct {
	dnsServices map[string]dnsclient.DnsClient
	config      Config
}

type Config struct {
	APIKey     string
	InstanceID string
	UserAgent  string
	Zones      []string
}

const ZONEIDFORTESTING = "1357a51b-f6ba-4bd4-a8a7-dd509499c08f"

func NewProvider(config Config) (*Provider, error) {
	if len(config.Zones) < 1 {
		return nil, fmt.Errorf("missing zone data")
	}

	provider := &Provider{}
	provider.dnsServices = make(map[string]dnsclient.DnsClient)

	authenticator := &core.IamAuthenticator{
		ApiKey: config.APIKey,
	}
	for _, zone := range config.Zones {
		options := &dnssvcsv1.DnsSvcsV1Options{
			Authenticator: authenticator,
			URL:           "https://api.dns-svcs.cloud.ibm.com/v1",
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
	return p.createOrUpdateDNSRecord(record, zone)
}

func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.createOrUpdateDNSRecord(record, zone)
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if err := validateInputDNSData(record, zone); err != nil {
		return fmt.Errorf("delete: invalid dns input data: %w", err)
	}
	dnsService, ok := p.dnsServices[zone.ID]
	if !ok {
		return fmt.Errorf("delete: unknown zone: %v", ZONEIDFORTESTING)
	}
	listOpt := dnsService.NewListResourceRecordsOptions(p.config.InstanceID, ZONEIDFORTESTING)
	// Some dns records (e.g. wildcard record) have an ending "." character in the DNSName
	DNSName := strings.TrimSuffix(record.Spec.DNSName, ".")

	result, response, err := dnsService.ListResourceRecords(listOpt)
	if err != nil {
		if response != nil && response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("delete: failed to list the dns record: %w", err)
		}
	}
	if result == nil {
		return fmt.Errorf("delete: invalid result")
	}
	for _, target := range record.Spec.Targets {
		for _, resourceRecord := range result.ResourceRecords {

			var resourceRecordTarget string
			if *resourceRecord.Type == "CNAME" {
				rData, ok := resourceRecord.Rdata.(map[string]interface{})
				if !ok {
					return fmt.Errorf("delete: failed to get resource data: %v", resourceRecord.Rdata)
				}
				resourceRecordTarget = fmt.Sprint(rData["cname"])
			}
			if *resourceRecord.Type == "A" {
				rData, ok := resourceRecord.Rdata.(map[string]interface{})
				if !ok {
					return fmt.Errorf("delete: A record - failed to get resource data: %v", resourceRecord.Rdata)
				}
				resourceRecordTarget = fmt.Sprint(rData["ip"])
			}

			if resourceRecordTarget == target && *resourceRecord.Name == DNSName {
				delOpt := dnsService.NewDeleteResourceRecordOptions(p.config.InstanceID, ZONEIDFORTESTING, *resourceRecord.ID)
				delResponse, err := dnsService.DeleteResourceRecord(delOpt)
				if err != nil {
					if delResponse != nil && delResponse.StatusCode != http.StatusNotFound {
						return fmt.Errorf("delete: failed to delete the dns record: %w", err)
					}
				}
				if delResponse != nil && delResponse.StatusCode != http.StatusNotFound {
					log.Info("deleted DNS record", "record", record, "zone", zone, "target", target)
				}
			}
		}
	}

	return nil
}

// validateDNSServices validates that provider clients can communicate with
// associated API endpoints by having each client list zones of the instance.
func validateDNSServices(provider *Provider) error {
	var errs []error
	for _, dnsService := range provider.dnsServices {

		// listDnszoneOptions := dnsService.NewListDnszonesOptions(provider.config.InstanceID)
		// listResult, response, reqErr := dnsService.ListDnszones(listDnszoneOptions)
		// if reqErr != nil {
		// 	errs = append(errs, fmt.Errorf("failed to get dns zones: %v", reqErr))
		// }
		// if response != nil {
		// 	fmt.Println("Response: ", response)
		// }

		// Maybe change/remove later
		getDnszoneOptions := dnsService.NewGetDnszoneOptions(
			provider.config.InstanceID,
			ZONEIDFORTESTING)
		result, response, reqErr := dnsService.GetDnszone(getDnszoneOptions)
		if reqErr != nil {
			panic(reqErr)
		}
		fmt.Printf("ID: %s", *result.ID)
		fmt.Printf("Response: %s", response)

	}
	return kerrors.NewAggregate(errs)
}

func (p *Provider) createOrUpdateDNSRecord(record *iov1.DNSRecord, zone configv1.DNSZone) error {

	if err := validateInputDNSData(record, zone); err != nil {
		return fmt.Errorf("createOrUpdateDNSRecord: invalid dns input data: %w", err)
	}
	dnsService, ok := p.dnsServices[zone.ID]
	if !ok {
		return fmt.Errorf("createOrUpdateDNSRecord: unknown zone: %v", zone.ID)
	}

	listOpt := dnsService.NewListResourceRecordsOptions(p.config.InstanceID, ZONEIDFORTESTING)
	// Some dns records (e.g. wildcard record) have an ending "." character in the DNSName
	DNSName := strings.TrimSuffix(record.Spec.DNSName, ".")

	// TTL must be one of [1 60 120 300 600 900 1800 3600 7200 18000 43200]
	useDefaultTTL := true
	for _, v := range []int64{1, 60, 120, 300, 600, 900, 1800, 3600, 7200, 18000, 43200} {
		if v == record.Spec.RecordTTL {
			useDefaultTTL = false
		}
	}
	if useDefaultTTL {
		log.Info("Warning: TTL must be one of [1 60 120 300 600 900 1800 3600 7200 18000 43200]. RecordTTL set to default", "default DSNSVCS record TTL", defaultDNSSVCSRecordTTL)
		record.Spec.RecordTTL = defaultDNSSVCSRecordTTL
	}

	for _, target := range record.Spec.Targets {
		update := false

		listResult, response, err := dnsService.ListResourceRecords(listOpt)
		if err != nil {
			if response != nil && response.StatusCode != http.StatusNotFound {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to list the dns record: %w", err)
			}
		}
		if listResult == nil {
			return fmt.Errorf("createOrUpdateDNSRecord: invalid result")
		}

		for _, resourceRecord := range listResult.ResourceRecords {

			var resourceRecordTarget string
			if *resourceRecord.Type == "CNAME" {
				rData, ok := resourceRecord.Rdata.(map[string]interface{})
				if !ok {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to get resource data: %v", resourceRecord.Rdata)
				}
				resourceRecordTarget = fmt.Sprint(rData["cname"])
			}
			if *resourceRecord.Type == "A" {
				fmt.Println("testing")
				fmt.Println(resourceRecord.Rdata)
				rData, ok := resourceRecord.Rdata.(map[string]interface{})
				if !ok {
					return fmt.Errorf("createOrUpdateDNSRecord: A record - failed to get resource data: %v", resourceRecord.Rdata)
				}
				resourceRecordTarget = fmt.Sprint(rData["ip"])
			}

			if *resourceRecord.Name == DNSName && resourceRecordTarget == target {
				update = true

				updateOpt := dnsService.NewUpdateResourceRecordOptions(p.config.InstanceID, ZONEIDFORTESTING, *resourceRecord.ID)
				updateOpt.SetName(DNSName)
				if record.Spec.RecordType == iov1.CNAMERecordType {
					inputRData, error := dnsService.NewResourceRecordUpdateInputRdataRdataCnameRecord(target)
					if error != nil {
						return fmt.Errorf("createOrUpdateDNSRecord: failed to create CNAME inputRData for the dns record: %w", err)
					}
					updateOpt.SetRdata(inputRData)
				} else {
					inputRData, error := dnsService.NewResourceRecordUpdateInputRdataRdataARecord(target)
					if error != nil {
						return fmt.Errorf("createOrUpdateDNSRecord: failed to create A inputRData for the dns record: %w", err)
					}
					updateOpt.SetRdata(inputRData)
				}
				updateOpt.SetTTL(record.Spec.RecordTTL)
				_, _, err := dnsService.UpdateResourceRecord(updateOpt)
				if err != nil {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to update the dns record: %w", err)
				}
				log.Info("updated DNS record", "record", record.Spec, "zone", zone, "target", target)
			}

		}
		if !update {
			createOpt := dnsService.NewCreateResourceRecordOptions(p.config.InstanceID, ZONEIDFORTESTING)
			createOpt.SetName(DNSName)
			createOpt.SetType(string(record.Spec.RecordType))

			if record.Spec.RecordType == iov1.CNAMERecordType {
				inputRData, error := dnsService.NewResourceRecordInputRdataRdataCnameRecord(target)
				if error != nil {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to create CNAME inputRData for the dns record: %w", err)
				}
				createOpt.SetRdata(inputRData)
			} else {
				inputRData, error := dnsService.NewResourceRecordInputRdataRdataARecord(target)
				if error != nil {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to create A inputRData for the dns record: %w", err)
				}
				createOpt.SetRdata(inputRData)
			}
			createOpt.SetTTL(record.Spec.RecordTTL)
			_, _, err := dnsService.CreateResourceRecord(createOpt)
			if err != nil {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to create the dns record: %w", err)
			}
			log.Info("created DNS record", "record", record.Spec, "zone", zone, "target", target)
		}
	}

	return nil
}

func validateInputDNSData(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	var errs []error
	if record == nil {
		errs = append(errs, fmt.Errorf("validateInputDNSData: dns record is nil"))
	} else {
		if len(record.Spec.DNSName) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record name is empty"))
		}
		if len(record.Spec.RecordType) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record type is empty"))
		}
		if len(record.Spec.Targets) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record content is empty"))
		}
	}
	if len(zone.ID) == 0 {
		errs = append(errs, fmt.Errorf("validateInputDNSData: dns zone id is empty"))
	}
	return kerrors.NewAggregate(errs)
}
