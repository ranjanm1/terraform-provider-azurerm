package compute

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-07-01/compute"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func linuxSecretSchema() *schema.Schema {
	return &schema.Schema{
		Type:     schema.TypeList,
		Optional: true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				// whilst this isn't present in the nested object it's required when this is specified
				"key_vault_id": {
					Type:         schema.TypeString,
					Required:     true,
					ValidateFunc: azure.ValidateResourceID,
				},

				// whilst we /could/ flatten this to `certificate_urls` we're intentionally not to keep this
				// closer to the Windows VMSS resource, which will also take a `store` param
				"certificate": {
					Type:     schema.TypeSet,
					Required: true,
					MinItems: 1,
					Elem: &schema.Resource{
						Schema: map[string]*schema.Schema{
							"url": {
								Type:         schema.TypeString,
								Required:     true,
								ValidateFunc: azure.ValidateKeyVaultChildId,
							},
						},
					},
				},
			},
		},
	}
}

func expandLinuxSecrets(input []interface{}) *[]compute.VaultSecretGroup {
	output := make([]compute.VaultSecretGroup, 0)

	for _, raw := range input {
		v := raw.(map[string]interface{})

		keyVaultId := v["key_vault_id"].(string)
		certificatesRaw := v["certificate"].(*schema.Set).List()
		certificates := make([]compute.VaultCertificate, 0)
		for _, certificateRaw := range certificatesRaw {
			certificateV := certificateRaw.(map[string]interface{})

			url := certificateV["url"].(string)
			certificates = append(certificates, compute.VaultCertificate{
				CertificateURL: utils.String(url),
			})
		}

		output = append(output, compute.VaultSecretGroup{
			SourceVault: &compute.SubResource{
				ID: utils.String(keyVaultId),
			},
			VaultCertificates: &certificates,
		})
	}

	return &output
}

func flattenLinuxSecrets(input *[]compute.VaultSecretGroup) []interface{} {
	if input == nil {
		return []interface{}{}
	}

	output := make([]interface{}, 0)

	for _, v := range *input {
		keyVaultId := ""
		if v.SourceVault != nil && v.SourceVault.ID != nil {
			keyVaultId = *v.SourceVault.ID
		}

		certificates := make([]interface{}, 0)

		if v.VaultCertificates != nil {
			for _, c := range *v.VaultCertificates {
				if c.CertificateURL == nil {
					continue
				}

				certificates = append(certificates, map[string]interface{}{
					"url": *c.CertificateURL,
				})
			}
		}

		output = append(output, map[string]interface{}{
			"key_vault_id": keyVaultId,
			"certificate":  certificates,
		})
	}

	return output
}

func SourceImageReferenceSchema() *schema.Schema {
	// whilst originally I was hoping we could use the 'id' from `azurerm_platform_image' unfortunately Azure doesn't
	// like this as a value for the 'id' field:
	// Id /...../Versions/16.04.201909091 is not a valid resource reference."
	// as such the image is split into two fields (source_image_id and source_image_reference) to provide better validation
	return &schema.Schema{
		Type:          schema.TypeList,
		Optional:      true,
		MaxItems:      1,
		ConflictsWith: []string{"source_image_id"},
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"publisher": {
					Type:     schema.TypeString,
					Required: true,
				},
				"offer": {
					Type:     schema.TypeString,
					Required: true,
				},
				"sku": {
					Type:     schema.TypeString,
					Required: true,
				},
				"version": {
					Type:     schema.TypeString,
					Required: true,
				},
			},
		},
	}
}

func ExpandSourceImageReference(referenceInput []interface{}, imageId string) (*compute.ImageReference, error) {
	if imageId != "" {
		return &compute.ImageReference{
			ID: utils.String(imageId),
		}, nil
	}

	if len(referenceInput) == 0 {
		return nil, fmt.Errorf("Either a `source_image_id` or a `source_image_reference` block must be specified!")
	}

	raw := referenceInput[0].(map[string]interface{})
	return &compute.ImageReference{
		Publisher: utils.String(raw["publisher"].(string)),
		Offer:     utils.String(raw["offer"].(string)),
		Sku:       utils.String(raw["sku"].(string)),
		Version:   utils.String(raw["version"].(string)),
	}, nil
}

func FlattenSourceImageReference(input *compute.ImageReference) []interface{} {
	// since the image id is pulled out as a separate field, if that's set we should return an empty block here
	if input == nil || input.ID != nil {
		return []interface{}{}
	}

	var publisher, offer, sku, version string

	if input.Publisher != nil {
		publisher = *input.Publisher
	}
	if input.Offer != nil {
		offer = *input.Offer
	}
	if input.Sku != nil {
		sku = *input.Sku
	}
	if input.Version != nil {
		version = *input.Version
	}

	return []interface{}{
		map[string]interface{}{
			"publisher": publisher,
			"offer":     offer,
			"sku":       sku,
			"version":   version,
		},
	}
}

func windowsSecretSchema() *schema.Schema {
	return &schema.Schema{
		Type:     schema.TypeList,
		Optional: true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				// whilst this isn't present in the nested object it's required when this is specified
				"key_vault_id": {
					Type:         schema.TypeString,
					Required:     true,
					ValidateFunc: azure.ValidateResourceID,
				},

				"certificate": {
					Type:     schema.TypeSet,
					Required: true,
					MinItems: 1,
					Elem: &schema.Resource{
						Schema: map[string]*schema.Schema{
							"store": {
								Type:     schema.TypeString,
								Required: true,
							},
							"url": {
								Type:         schema.TypeString,
								Required:     true,
								ValidateFunc: azure.ValidateKeyVaultChildId,
							},
						},
					},
				},
			},
		},
	}
}

func expandWindowsSecrets(input []interface{}) *[]compute.VaultSecretGroup {
	output := make([]compute.VaultSecretGroup, 0)

	for _, raw := range input {
		v := raw.(map[string]interface{})

		keyVaultId := v["key_vault_id"].(string)
		certificatesRaw := v["certificate"].(*schema.Set).List()
		certificates := make([]compute.VaultCertificate, 0)
		for _, certificateRaw := range certificatesRaw {
			certificateV := certificateRaw.(map[string]interface{})

			store := certificateV["store"].(string)
			url := certificateV["url"].(string)
			certificates = append(certificates, compute.VaultCertificate{
				CertificateStore: utils.String(store),
				CertificateURL:   utils.String(url),
			})
		}

		output = append(output, compute.VaultSecretGroup{
			SourceVault: &compute.SubResource{
				ID: utils.String(keyVaultId),
			},
			VaultCertificates: &certificates,
		})
	}

	return &output
}

func flattenWindowsSecrets(input *[]compute.VaultSecretGroup) []interface{} {
	if input == nil {
		return []interface{}{}
	}

	output := make([]interface{}, 0)

	for _, v := range *input {
		keyVaultId := ""
		if v.SourceVault != nil && v.SourceVault.ID != nil {
			keyVaultId = *v.SourceVault.ID
		}

		certificates := make([]interface{}, 0)

		if v.VaultCertificates != nil {
			for _, c := range *v.VaultCertificates {
				store := ""
				if c.CertificateStore != nil {
					store = *c.CertificateStore
				}

				url := ""
				if c.CertificateURL != nil {
					url = *c.CertificateURL
				}

				certificates = append(certificates, map[string]interface{}{
					"store": store,
					"url":   url,
				})
			}
		}

		output = append(output, map[string]interface{}{
			"key_vault_id": keyVaultId,
			"certificate":  certificates,
		})
	}

	return output
}
