package compute

import (
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-07-01/compute"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/tf"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/validate"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/clients"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/features"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/locks"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tags"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tf/base64"
	azSchema "github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tf/schema"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/timeouts"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

// TODO: locking as appropriate

func resourceLinuxVirtualMachine() *schema.Resource {
	return &schema.Resource{
		Create: resourceLinuxVirtualMachineCreate,
		Read:   resourceLinuxVirtualMachineRead,
		Update: resourceLinuxVirtualMachineUpdate,
		Delete: resourceLinuxVirtualMachineDelete,
		Importer: azSchema.ValidateResourceIDPriorToImport(func(id string) error {
			_, err := ParseVirtualMachineID(id)
			// TODO: confirm prior to the Beta that this is a Linux VM
			// TODO: confirm that the OS Disk isn't "attach"
			return err
		}),

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(45 * time.Minute),
			Read:   schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(45 * time.Minute),
			Delete: schema.DefaultTimeout(45 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: ValidateLinuxName,
			},

			"resource_group_name": azure.SchemaResourceGroupName(),

			"location": azure.SchemaLocation(),

			// Required
			"admin_username": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validate.NoEmptyStrings,
			},

			"network_interface_ids": {
				Type:     schema.TypeList,
				Required: true,
				MinItems: 1,
				Elem: &schema.Schema{
					Type: schema.TypeString,
					// TODO: validate is a NIC Resource ID
				},
			},

			"os_disk": VirtualMachineOSDiskSchema(),

			"size": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validate.NoEmptyStrings,
			},

			// Optional
			"additional_capabilities": virtualMachineAdditionalCapabilitiesSchema(),

			"admin_password": {
				Type:      schema.TypeString,
				Optional:  true,
				ForceNew:  true,
				Sensitive: true,
			},

			"admin_ssh_key": SSHKeysSchema(),

			"allow_extension_operations": {
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: true, // TODO: confirm behaviour
				Default:  true,
			},

			"availability_set_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				// TODO: it'd be nice to add more granular validation here
				ValidateFunc: azure.ValidateResourceID,
				// TODO: confirm if the casing is also broken for this API
				ConflictsWith: []string{
					// TODO: "virtual_machine_scale_set_id"
					"zone",
				},
			},

			"boot_diagnostics": bootDiagnosticsSchema(),

			"computer_name": {
				Type:     schema.TypeString,
				Optional: true,

				// Computed since we reuse the VM name if one's not specified
				Computed: true,
				ForceNew: true,
				// note: whilst the portal says 1-15 characters it seems to mirror the rules for the vm name
				// (e.g. 1-15 for Windows, 1-63 for Linux)
				ValidateFunc: ValidateLinuxName,
			},

			"custom_data": base64.OptionalSchema(),

			"disable_password_authentication": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
			},

			"priority": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  string(compute.Regular),
				ValidateFunc: validation.StringInSlice([]string{
					string(compute.Low), // TODO: remove me
					string(compute.Regular),
					// TODO: spot
				}, false),
			},

			"provision_vm_agent": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: true,
			},

			"proximity_placement_group_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				// TODO: it'd be nice to add more granular validation here
				ValidateFunc: azure.ValidateResourceID,
				// TODO: confirm if also the case for the VM API
				// the Compute API is broken and returns the Resource Group name in UPPERCASE :shrug:
				//DiffSuppressFunc: suppress.CaseDifference,
			},

			"secret": linuxSecretSchema(),

			"source_image_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: azure.ValidateResourceID,
			},

			"source_image_reference": SourceImageReferenceSchema(),

			"tags": tags.Schema(),

			"zone": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				ConflictsWith: []string{
					// TODO: confirm is this right?
					"availability_set_id",
					// TODO: "virtual_machine_scale_set_id"
				},
			},

			// Computed
			"private_ip_address": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"private_ip_addresses": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"public_ip_address": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"public_ip_addresses": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"virtual_machine_id": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceLinuxVirtualMachineCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	ctx, cancel := timeouts.ForCreate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	name := d.Get("name").(string)
	resourceGroup := d.Get("resource_group_name").(string)

	locks.ByName(name, virtualMachineResourceName)
	defer locks.UnlockByName(name, virtualMachineResourceName)

	if features.ShouldResourcesBeImported() {
		resp, err := client.Get(ctx, resourceGroup, name, "")
		if err != nil {
			if !utils.ResponseWasNotFound(resp.Response) {
				return fmt.Errorf("Error checking for existing Linux Virtual Machine %q (Resource Group %q): %+v", name, resourceGroup, err)
			}
		}

		if !utils.ResponseWasNotFound(resp.Response) {
			return tf.ImportAsExistsError("azurerm_linux_virtual_machine", *resp.ID)
		}
	}

	additionalCapabilitiesRaw := d.Get("additional_capabilities").([]interface{})
	additionalCapabilities := expandVirtualMachineAdditionalCapabilities(additionalCapabilitiesRaw)

	adminUsername := d.Get("admin_username").(string)
	allowExtensionOperations := d.Get("allow_extension_operations").(bool)
	bootDiagnosticsRaw := d.Get("boot_diagnostics").([]interface{})
	bootDiagnostics := expandBootDiagnostics(bootDiagnosticsRaw)
	var computerName string
	if v, ok := d.GetOk("computer_name"); ok && len(v.(string)) > 0 {
		computerName = v.(string)
	} else {
		computerName = name
	}
	disablePasswordAuthentication := d.Get("disable_password_authentication").(bool)
	location := azure.NormalizeLocation(d.Get("location").(string))
	priority := d.Get("priority").(string)
	provisionVMAgent := d.Get("provision_vm_agent").(bool)
	size := d.Get("size").(string)
	t := d.Get("tags").(map[string]interface{})

	networkInterfaceIdsRaw := d.Get("network_interface_ids").([]interface{})
	networkInterfaceIds := expandVirtualMachineNetworkInterfaceIDs(networkInterfaceIdsRaw)

	osDiskRaw := d.Get("os_disk").([]interface{})
	osDisk := ExpandVirtualMachineOSDisk(osDiskRaw, compute.Linux)

	secretsRaw := d.Get("secret").([]interface{})
	secrets := expandLinuxSecrets(secretsRaw)

	sourceImageReferenceRaw := d.Get("source_image_reference").([]interface{})
	sourceImageId := d.Get("source_image_id").(string)
	sourceImageReference, err := ExpandSourceImageReference(sourceImageReferenceRaw, sourceImageId)
	if err != nil {
		return err
	}

	sshKeysRaw := d.Get("admin_ssh_key").(*schema.Set).List()
	sshKeys := ExpandSSHKeys(sshKeysRaw)

	params := compute.VirtualMachine{
		Name:     utils.String(name),
		Location: utils.String(location),
		VirtualMachineProperties: &compute.VirtualMachineProperties{
			HardwareProfile: &compute.HardwareProfile{
				VMSize: compute.VirtualMachineSizeTypes(size),
			},
			OsProfile: &compute.OSProfile{
				AdminUsername:            utils.String(adminUsername),
				ComputerName:             utils.String(computerName),
				AllowExtensionOperations: utils.Bool(allowExtensionOperations),
				LinuxConfiguration: &compute.LinuxConfiguration{
					DisablePasswordAuthentication: utils.Bool(disablePasswordAuthentication),
					ProvisionVMAgent:              utils.Bool(provisionVMAgent),
					SSH: &compute.SSHConfiguration{
						PublicKeys: &sshKeys,
					},
				},
				Secrets: secrets,
			},
			NetworkProfile: &compute.NetworkProfile{
				NetworkInterfaces: &networkInterfaceIds,
			},
			Priority: compute.VirtualMachinePriorityTypes(priority),
			StorageProfile: &compute.StorageProfile{
				ImageReference: sourceImageReference,
				OsDisk:         osDisk,

				// Data Disks are instead handled via the Association resource - as such we can send an empty value here
				// but for Updates this'll need to be nil, else any associations will be overwritten
				DataDisks: &[]compute.DataDisk{},
			},

			// Optional
			AdditionalCapabilities: additionalCapabilities,
			DiagnosticsProfile:     bootDiagnostics,

			// conflicts with availability set id
			//VirtualMachineScaleSet: nil,

			// Optional
			//BillingProfile: nil,
			//EvictionPolicy: "",

			// Optional - dedicated_host_id
			Host: nil,

			// only applicable to Windows
			//LicenseType:             utils.String(licenseType),
		},
		Tags: tags.Expand(t),
		// TODO: optionally populated
		//Identity:                 nil,
		//Plan:                     nil,
	}

	if !provisionVMAgent && allowExtensionOperations {
		return fmt.Errorf("`allow_extension_operations` cannot be set to `true` when `provision_vm_agent` is set to `false`")
	}

	if v, ok := d.GetOk("availability_set_id"); ok {
		params.AvailabilitySet = &compute.SubResource{
			ID: utils.String(v.(string)),
		}
	}

	if v, ok := d.GetOk("custom_data"); ok {
		params.OsProfile.CustomData = utils.String(v.(string))
	}

	if v, ok := d.GetOk("proximity_placement_group_id"); ok {
		params.ProximityPlacementGroup = &compute.SubResource{
			ID: utils.String(v.(string)),
		}
	}

	if v, ok := d.GetOk("zone"); ok {
		params.Zones = &[]string{
			v.(string),
		}
	}

	// "Authentication using either SSH or by user name and password must be enabled in Linux profile." Target="linuxConfiguration"
	adminPassword := d.Get("admin_password").(string)
	if disablePasswordAuthentication && len(sshKeys) == 0 {
		return fmt.Errorf("At least one `admin_ssh_key` must be specified when `disable_password_authentication` is set to `true`")
	} else if !disablePasswordAuthentication {
		if adminPassword == "" {
			return fmt.Errorf("An `admin_password` must be specified if `disable_password_authentication` is set to `false`")
		}

		params.OsProfile.AdminPassword = utils.String(adminPassword)
	}

	future, err := client.CreateOrUpdate(ctx, resourceGroup, name, params)
	if err != nil {
		return fmt.Errorf("Error creating Linux Virtual Machine %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if err := future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("Error waiting for creation of Linux Virtual Machine %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	read, err := client.Get(ctx, resourceGroup, name, "")
	if err != nil {
		return fmt.Errorf("Error retrieving Linux Virtual Machine %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if read.ID == nil {
		return fmt.Errorf("Error retrieving Linux Virtual Machine %q (Resource Group %q): `id` was nil", name, resourceGroup)
	}

	d.SetId(*read.ID)
	setVirtualMachineConnectionInformation(d, read.VirtualMachineProperties)
	return resourceLinuxVirtualMachineRead(d, meta)
}

func resourceLinuxVirtualMachineRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := ParseVirtualMachineID(d.Id())
	if err != nil {
		return err
	}

	resp, err := client.Get(ctx, id.ResourceGroup, id.Name, "")
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[DEBUG] Linux Virtual Machine %q was not found in Resource Group %q - removing from state!", id.Name, id.ResourceGroup)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error retrieving Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}

	d.Set("name", id.Name)
	d.Set("resource_group_name", id.ResourceGroup)
	if location := resp.Location; location != nil {
		d.Set("location", azure.NormalizeLocation(*resp.Location))
	}

	if props := resp.VirtualMachineProperties; props != nil {
		if err := d.Set("additional_capabilities", flattenVirtualMachineAdditionalCapabilities(props.AdditionalCapabilities)); err != nil {
			return fmt.Errorf("Error setting `additional_capabilities`: %+v", err)
		}

		availabilitySetId := ""
		if props.AvailabilitySet != nil && props.AvailabilitySet.ID != nil {
			availabilitySetId = *props.AvailabilitySet.ID
		}
		d.Set("availability_set_id", availabilitySetId)

		if err := d.Set("boot_diagnostics", flattenBootDiagnostics(props.DiagnosticsProfile)); err != nil {
			return fmt.Errorf("Error setting `boot_diagnostics`: %+v", err)
		}

		if profile := props.HardwareProfile; profile != nil {
			d.Set("size", string(profile.VMSize))
		}

		if profile := props.NetworkProfile; profile != nil {
			if err := d.Set("network_interface_ids", flattenVirtualMachineNetworkInterfaceIDs(props.NetworkProfile.NetworkInterfaces)); err != nil {
				return fmt.Errorf("Error setting `network_interface_ids`: %+v", err)
			}
		}

		//dedicatedHostId := ""
		//if props.Host != nil && props.Host.ID != nil {
		//	dedicatedHostId = *props.Host.ID
		//}
		//d.Set("dedicated_host_id", dedicatedHostId)

		if profile := props.OsProfile; profile != nil {
			d.Set("admin_username", profile.AdminUsername)
			d.Set("allow_extension_operations", profile.AllowExtensionOperations)
			d.Set("computer_name", profile.ComputerName)

			if config := profile.LinuxConfiguration; config != nil {
				d.Set("disable_password_authentication", config.DisablePasswordAuthentication)
				d.Set("provision_vm_agent", config.ProvisionVMAgent)

				flattenedSSHKeys, err := FlattenSSHKeys(config.SSH)
				if err != nil {
					return fmt.Errorf("Error flattening `admin_ssh_key`: %+v", err)
				}
				if err := d.Set("admin_ssh_key", flattenedSSHKeys); err != nil {
					return fmt.Errorf("Error setting `admin_ssh_key`: %+v", err)
				}
			}

			if err := d.Set("secret", flattenLinuxSecrets(profile.Secrets)); err != nil {
				return fmt.Errorf("Error setting `secret`: %+v", err)
			}
		}

		d.Set("priority", string(props.Priority))
		proximityPlacementGroupId := ""
		if props.ProximityPlacementGroup != nil && props.ProximityPlacementGroup.ID != nil {
			proximityPlacementGroupId = *props.ProximityPlacementGroup.ID
		}
		d.Set("proximity_placement_group_id", proximityPlacementGroupId)

		if profile := props.StorageProfile; profile != nil {
			if err := d.Set("os_disk", FlattenVirtualMachineOSDisk(profile.OsDisk)); err != nil {
				return fmt.Errorf("Error settings `os_disk`: %+v", err)
			}

			var storageImageId string
			if profile.ImageReference != nil && profile.ImageReference.ID != nil {
				storageImageId = *profile.ImageReference.ID
			}
			d.Set("source_image_id", storageImageId)

			if err := d.Set("source_image_reference", FlattenSourceImageReference(profile.ImageReference)); err != nil {
				return fmt.Errorf("Error setting `source_image_reference`: %+v", err)
			}
		}

		// Computed - TODO: implement me
		d.Set("private_ip_address", "")
		d.Set("private_ip_addresses", []interface{}{})
		d.Set("public_ip_address", "")
		d.Set("public_ip_addresses", []interface{}{})
		d.Set("virtual_machine_id", props.VMID)

	}

	zone := ""
	if resp.Zones != nil {
		if zones := *resp.Zones; len(zones) > 0 {
			zone = zones[0]
		}
	}
	d.Set("zone", zone)

	setVirtualMachineConnectionInformation(d, resp.VirtualMachineProperties)

	return tags.FlattenAndSet(d, resp.Tags)
}

func resourceLinuxVirtualMachineUpdate(d *schema.ResourceData, meta interface{}) error {
	//client := meta.(*clients.Client).Compute.VMClient
	//ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	//defer cancel()

	//id, err := ParseVirtualMachineID(d.Id())
	//if err != nil {
	//	return err
	//}
	//
	//locks.ByName(id.Name, virtualMachineResourceName)
	//defer locks.UnlockByName(id.Name, virtualMachineResourceName)
	//
	//params := compute.VirtualMachineUpdate{}
	//
	//shouldShutDown := false
	//shouldTurnBackOn := true // TODO: unless this was already shut-down, in which case do nothing
	//
	//if d.HasChange("network_interface_ids") {
	//	log.Printf("[DEBUG] Updating the Network Interfaces for Virtual Machine %q (Resource Group %q)..", id.Name, id.ResourceGroup)
	//	// TODO: do we need to stop the Virtual Machine to make these changes?
	//	// client.Update(..)
	//	log.Printf("[DEBUG] Updated the Network Interfaces for Virtual Machine %q (Resource Group %q).", id.Name, id.ResourceGroup)
	//}

	// setVirtualMachineConnectionInformation(d, read.VirtualMachineProperties)

	return resourceLinuxVirtualMachineRead(d, meta)
}

func resourceLinuxVirtualMachineDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := ParseVirtualMachineID(d.Id())
	if err != nil {
		return err
	}

	locks.ByName(id.Name, virtualMachineResourceName)
	defer locks.UnlockByName(id.Name, virtualMachineResourceName)

	log.Printf("[DEBUG] Retrieving Linux Virtual Machine %q (Resource Group %q)..", id.Name, id.ResourceGroup)
	existing, err := client.Get(ctx, id.ResourceGroup, id.Name, "")
	if err != nil {
		if utils.ResponseWasNotFound(existing.Response) {
			return nil
		}

		return fmt.Errorf("Error retrieving Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}

	// ISSUE: XXX
	// shutting down the Virtual Machine prior to removing it means users are no longer charged for the compute
	// thus this can be a large cost-saving when deleting larger instances
	// in addition - since we're shutting down the machine to remove it, forcing a power-off is fine (as opposed
	// to waiting for a graceful shut down)
	log.Printf("[DEBUG] Powering Off Linux Virtual Machine %q (Resource Group %q)..", id.Name, id.ResourceGroup)
	skipShutdown := true
	powerOffFuture, err := client.PowerOff(ctx, id.ResourceGroup, id.Name, utils.Bool(skipShutdown))
	if err != nil {
		return fmt.Errorf("Error powering off Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}
	if err := powerOffFuture.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("Error waiting for power off of Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}
	log.Printf("[DEBUG] Powered Off Linux Virtual Machine %q (Resource Group %q).", id.Name, id.ResourceGroup)

	log.Printf("[DEBUG] Deleting Linux Virtual Machine %q (Resource Group %q)..", id.Name, id.ResourceGroup)
	deleteFuture, err := client.Delete(ctx, id.ResourceGroup, id.Name)
	if err != nil {
		return fmt.Errorf("Error deleting Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}
	if err := deleteFuture.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("Error waiting for deletion of Linux Virtual Machine %q (Resource Group %q): %+v", id.Name, id.ResourceGroup, err)
	}
	log.Printf("[DEBUG] Deleted Linux Virtual Machine %q (Resource Group %q).", id.Name, id.ResourceGroup)

	return nil
}
