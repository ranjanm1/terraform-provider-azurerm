package azurerm

import (
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/arm/containerinstance"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmContainerGroup() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmContainerGroupCreate,
		Read:   resourceArmContainerGroupRead,
		Delete: resourceArmContainerGroupDelete,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"location": locationSchema(),

			"resource_group_name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"ip_address_type": {
				Type:             schema.TypeString,
				Optional:         true,
				Default:          "Public",
				ForceNew:         true,
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
				ValidateFunc: validation.StringInSlice([]string{
					"Public",
				}, true),
			},

			"os_type": {
				Type:             schema.TypeString,
				Required:         true,
				ForceNew:         true,
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
				ValidateFunc: validation.StringInSlice([]string{
					"windows",
					"linux",
				}, true),
			},

			"tags": tagsForceNewSchema(),

			"ip_address": {
				Type:     schema.TypeString,
				Computed: true,
				ForceNew: true,
			},

			"container": {
				Type:     schema.TypeList,
				Required: true,
				ForceNew: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},

						"image": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},

						"cpu": {
							Type:     schema.TypeFloat,
							Required: true,
							ForceNew: true,
						},

						"memory": {
							Type:     schema.TypeFloat,
							Required: true,
							ForceNew: true,
						},

						"port": {
							Type:         schema.TypeInt,
							Optional:     true,
							ForceNew:     true,
							ValidateFunc: validation.IntBetween(1, 65535),
						},

						"protocol": {
							Type:             schema.TypeString,
							Optional:         true,
							ForceNew:         true,
							DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
							ValidateFunc: validation.StringInSlice([]string{
								"tcp",
								"udp",
							}, true),
						},

						"env_var": {
							Type:     schema.TypeList,
							Optional: true,
							ForceNew: true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"name": {
										Type:     schema.TypeString,
										Required: true,
										ForceNew: true,
									},

									"value": {
										Type:     schema.TypeString,
										Required: true,
										ForceNew: true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func resourceArmContainerGroupCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient)
	containerGroupsClient := client.containerGroupsClient

	// container group properties
	resGroup := d.Get("resource_group_name").(string)
	name := d.Get("name").(string)
	location := d.Get("location").(string)
	OSType := d.Get("os_type").(string)
	IPAddressType := d.Get("ip_address_type").(string)
	tags := d.Get("tags").(map[string]interface{})

	containers, containerGroupPorts := expandContainerGroupContainers(d)

	containerGroup := containerinstance.ContainerGroup{
		Name:     &name,
		Location: &location,
		Tags:     expandTags(tags),
		ContainerGroupProperties: &containerinstance.ContainerGroupProperties{
			Containers: containers,
			IPAddress: &containerinstance.IPAddress{
				Type:  &IPAddressType,
				Ports: containerGroupPorts,
			},
			OsType: containerinstance.OperatingSystemTypes(OSType),
		},
	}

	_, err := containerGroupsClient.CreateOrUpdate(resGroup, name, containerGroup)
	if err != nil {
		return err
	}

	read, err := containerGroupsClient.Get(resGroup, name)
	if err != nil {
		return err
	}

	if read.ID == nil {
		return fmt.Errorf("Cannot read container group %s (resource group %s) ID", name, resGroup)
	}

	d.SetId(*read.ID)

	return resourceArmContainerGroupRead(d, meta)
}
func resourceArmContainerGroupRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient)
	containterGroupsClient := client.containerGroupsClient

	id, err := parseAzureResourceID(d.Id())

	if err != nil {
		return err
	}

	resGroup := id.ResourceGroup
	name := id.Path["containerGroups"]

	resp, err := containterGroupsClient.Get(resGroup, name)

	if err != nil {
		return err
	}

	d.Set("name", name)
	d.Set("resource_group_name", resGroup)
	d.Set("location", azureRMNormalizeLocation(*resp.Location))
	flattenAndSetTags(d, resp.Tags)

	d.Set("os_type", string(resp.OsType))
	if address := resp.IPAddress; address != nil {
		d.Set("ip_address_type", address.Type)
		d.Set("ip_address", address.IP)
	}

	containerConfigs := flattenContainerGroupContainers(resp.Containers)
	d.Set("container", containerConfigs)

	return nil
}

func resourceArmContainerGroupDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient)
	containterGroupsClient := client.containerGroupsClient

	id, err := parseAzureResourceID(d.Id())

	if err != nil {
		return err
	}

	// container group properties
	resGroup := id.ResourceGroup
	name := id.Path["containerGroups"]

	resp, err := containterGroupsClient.Delete(resGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			return nil
		}
		return err
	}

	return nil
}

func flattenContainerGroupContainers(containers *[]containerinstance.Container) []interface{} {

	containerConfigs := make([]interface{}, 0, len(*containers))
	for _, container := range *containers {
		containerConfig := make(map[string]interface{})
		containerConfig["name"] = *container.Name
		containerConfig["image"] = *container.Image

		resourceRequests := *(*container.Resources).Requests
		containerConfig["cpu"] = *resourceRequests.CPU
		containerConfig["memory"] = *resourceRequests.MemoryInGB

		if len(*container.Ports) > 0 {
			containerConfig["port"] = *(*container.Ports)[0].Port
		}
		// protocol isn't returned in container config

		containerConfigs = append(containerConfigs, containerConfig)
	}

	return containerConfigs
}

func expandContainerGroupContainers(d *schema.ResourceData) (*[]containerinstance.Container, *[]containerinstance.Port) {
	containersConfig := d.Get("container").([]interface{})
	containers := make([]containerinstance.Container, 0, len(containersConfig))
	containerGroupPorts := make([]containerinstance.Port, 0, len(containersConfig))

	for _, containerConfig := range containersConfig {
		data := containerConfig.(map[string]interface{})

		// required
		name := data["name"].(string)
		image := data["image"].(string)
		cpu := data["cpu"].(float64)
		memory := data["memory"].(float64)

		container := containerinstance.Container{
			Name: &name,
			ContainerProperties: &containerinstance.ContainerProperties{
				Image: &image,
				Resources: &containerinstance.ResourceRequirements{
					Requests: &containerinstance.ResourceRequests{
						MemoryInGB: &memory,
						CPU:        &cpu,
					},
				},
			},
		}

		if v, _ := data["port"]; v != 0 {
			port := int32(v.(int))

			// container port (port number)
			containerPort := containerinstance.ContainerPort{
				Port: &port,
			}

			container.Ports = &[]containerinstance.ContainerPort{containerPort}

			// container group port (port number + protocol)
			containerGroupPort := containerinstance.Port{
				Port: &port,
			}

			if v, ok := data["protocol"]; ok {
				protocol := v.(string)
				containerGroupPort.Protocol = containerinstance.ContainerGroupNetworkProtocol(strings.ToUpper(protocol))
			}

			containerGroupPorts = append(containerGroupPorts, containerGroupPort)
		}

		containers = append(containers, container)
	}

	return &containers, &containerGroupPorts
}
