package cloud

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
)

// awsResourceManager uses the AWS Go SDK. Docs can be found at:
// https://docs.aws.amazon.com/sdk-for-go/api/service/ec2/
type awsResourceManager struct {
	accounts []string
}

func (m *awsResourceManager) Owners() []string {
	return m.accounts
}

type awsInstance struct {
	baseInstance
}

// Cleanup will termiante this instance
func (i *awsInstance) Cleanup() error {
	log.Println("Cleaning up instance", i.ID())
	client := clientForAWSResource(i)
	input := &ec2.TerminateInstancesInput{
		InstanceIds: aws.StringSlice([]string{i.id}),
	}
	_, err := client.TerminateInstances(input)
	return err
}

func (i *awsInstance) SetTag(key, value string, overwrite bool) error {
	return addAWSTag(i, key, value, overwrite)
}

type awsImage struct {
	baseImage
}

func (i *awsImage) Cleanup() error {
	log.Println("Cleaning up image", i.ID())
	client := clientForAWSResource(i)
	input := &ec2.DeregisterImageInput{
		ImageId: aws.String(i.ID()),
	}
	_, err := client.DeregisterImage(input)
	return err
}

func (i *awsImage) SetTag(key, value string, overwrite bool) error {
	return addAWSTag(i, key, value, overwrite)
}

func (i *awsImage) MakePrivate() error {
	log.Println("Making image private:", i.ID())
	if !i.Public() {
		// Image is already private
		return nil
	}
	client := clientForAWSResource(i)
	input := &ec2.ModifyImageAttributeInput{
		ImageId: aws.String(i.ID()),
		LaunchPermission: &ec2.LaunchPermissionModifications{
			Remove: []*ec2.LaunchPermission{&ec2.LaunchPermission{
				Group: aws.String("all"),
			}},
		},
	}
	_, err := client.ModifyImageAttribute(input)
	if err != nil {
		return err
	}
	i.public = false
	return nil
}

type awsVolume struct {
	baseVolume
}

func (v *awsVolume) Cleanup() error {
	log.Println("Cleaning up volume", v.ID())
	client := clientForAWSResource(v)
	input := &ec2.DeleteVolumeInput{
		VolumeId: aws.String(v.ID()),
	}
	_, err := client.DeleteVolume(input)
	return err
}

func (v *awsVolume) SetTag(key, value string, overwrite bool) error {
	return addAWSTag(v, key, value, overwrite)
}

type awsSnapshot struct {
	baseSnapshot
}

func (s *awsSnapshot) Cleanup() error {
	log.Println("Cleaning up snapshot", s.ID())
	client := clientForAWSResource(s)
	input := &ec2.DeleteSnapshotInput{
		SnapshotId: aws.String(s.ID()),
	}
	_, err := client.DeleteSnapshot(input)
	return err
}

func (s *awsSnapshot) SetTag(key, value string, overwrite bool) error {
	return addAWSTag(s, key, value, overwrite)
}

const (
	assumeRoleARNTemplate = "arn:aws:iam::%s:role/brkt-HouseKeeper"

	accessDeniedErrorCode = "AccessDenied"
)

var (
	instanceStateFilterName = "instance-state-name"
	instanceStateRunning    = ec2.InstanceStateNameRunning

	awsOwnerIDSelfValue = "self"
)

func (m *awsResourceManager) InstancesPerAccount() map[string][]Instance {
	resultMap := make(map[string][]Instance)
	getAllEC2Resources(m.accounts, func(client *ec2.EC2, account string) {
		instances, err := getAWSInstances(account, client)
		if err != nil {
			handleAWSAccessDenied(account, err)
		} else if len(instances) > 0 {
			resultMap[account] = append(resultMap[account], instances...)
		}
	})
	return resultMap
}

func (m *awsResourceManager) ImagesPerAccount() map[string][]Image {
	resultMap := make(map[string][]Image)
	getAllEC2Resources(m.accounts, func(client *ec2.EC2, account string) {
		images, err := getAWSImages(account, client)
		if err != nil {
			handleAWSAccessDenied(account, err)
		} else if len(images) > 0 {
			resultMap[account] = append(resultMap[account], images...)
		}
	})
	return resultMap
}

func (m *awsResourceManager) VolumesPerAccount() map[string][]Volume {
	resultMap := make(map[string][]Volume)
	getAllEC2Resources(m.accounts, func(client *ec2.EC2, account string) {
		volumes, err := getAWSVolumes(account, client)
		if err != nil {
			handleAWSAccessDenied(account, err)
		} else if len(volumes) > 0 {
			resultMap[account] = append(resultMap[account], volumes...)
		}
	})
	return resultMap
}

func (m *awsResourceManager) SnapshotsPerAccount() map[string][]Snapshot {
	resultMap := make(map[string][]Snapshot)
	getAllEC2Resources(m.accounts, func(client *ec2.EC2, account string) {
		snapshots, err := getAWSSnapshots(account, client)
		if err != nil {
			handleAWSAccessDenied(account, err)
		} else if len(snapshots) > 0 {
			resultMap[account] = append(resultMap[account], snapshots...)
		}
	})
	return resultMap
}

func (m *awsResourceManager) AllResourcesPerAccount() map[string]*ResourceCollection {
	resultMap := make(map[string]*ResourceCollection)
	for i := range m.accounts {
		resultMap[m.accounts[i]] = new(ResourceCollection)
	}
	// TODO: Smarter error handling. If one request get access denied, then might as
	// well abort. The rest are going to fail too.
	getAllEC2Resources(m.accounts, func(client *ec2.EC2, account string) {
		result := resultMap[account]
		result.Owner = account
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			snapshots, err := getAWSSnapshots(account, client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			}
			result.Snapshots = append(result.Snapshots, snapshots...)
			wg.Done()
		}()
		go func() {
			instances, err := getAWSInstances(account, client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			}
			result.Instances = append(result.Instances, instances...)
			wg.Done()
		}()
		go func() {
			images, err := getAWSImages(account, client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			}
			result.Images = append(result.Images, images...)
			wg.Done()
		}()
		go func() {
			volumes, err := getAWSVolumes(account, client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			}
			result.Volumes = append(result.Volumes, volumes...)
			wg.Done()
		}()
		wg.Wait()
		resultMap[account] = result
	})
	return resultMap
}

func (m *awsResourceManager) CleanupInstances(instances []Instance) error {
	resList := []Resource{}
	for i := range instances {
		v, ok := instances[i].(Resource)
		if !ok {
			return errors.New("Could not convert Instance to Resource")
		}
		resList = append(resList, v)
	}
	return cleanupResources(resList)
}

func (m *awsResourceManager) CleanupImages(images []Image) error {
	resList := []Resource{}
	for i := range images {
		v, ok := images[i].(Resource)
		if !ok {
			return errors.New("Could not convert Image to Resource")
		}
		resList = append(resList, v)
	}
	return cleanupResources(resList)
}

func (m *awsResourceManager) CleanupVolumes(volumes []Volume) error {
	resList := []Resource{}
	for i := range volumes {
		v, ok := volumes[i].(Resource)
		if !ok {
			return errors.New("Could not convert Image to Resource")
		}
		resList = append(resList, v)
	}
	return cleanupResources(resList)
}

func (m *awsResourceManager) CleanupSnapshots(snapshots []Snapshot) error {
	resList := []Resource{}
	for i := range snapshots {
		v, ok := snapshots[i].(Resource)
		if !ok {
			return errors.New("Could not convert Image to Resource")
		}
		resList = append(resList, v)
	}
	return cleanupResources(resList)
}

func cleanupResources(resources []Resource) error {
	failed := false
	var wg sync.WaitGroup
	wg.Add(len(resources))
	for i := range resources {
		go func(index int) {
			err := resources[index].Cleanup()
			if err != nil {
				log.Printf("Cleaning up %s for owner %s failed\n", resources[index].ID(), resources[index].Owner())
				failed = true
			}
			wg.Done()
		}(i)
	}
	wg.Wait()
	if failed {
		return errors.New("One or more resource cleanups failed")
	}
	return nil
}

// getAWSInstances will get all running instances using an already
// set-up client for a specific credential and region.
func getAWSInstances(account string, client *ec2.EC2) ([]Instance, error) {
	// We're only interested in running instances
	input := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{&ec2.Filter{
			Name:   aws.String(instanceStateFilterName),
			Values: aws.StringSlice([]string{instanceStateRunning})}},
	}
	awsReservations, err := client.DescribeInstances(input)
	if err != nil {
		return nil, err
	}
	result := []Instance{}
	for _, reservation := range awsReservations.Reservations {
		for _, instance := range reservation.Instances {
			inst := awsInstance{baseInstance{
				baseResource: baseResource{
					csp:          AWS,
					owner:        account,
					id:           *instance.InstanceId,
					location:     *client.Config.Region,
					creationTime: *instance.LaunchTime,
					public:       instance.PublicIpAddress != nil,
					tags:         convertAWSTags(instance.Tags)},
				instanceType: *instance.InstanceType,
			}}
			result = append(result, &inst)
		}
	}
	return result, nil
}

// getAWSImages will get all AMIs owned by the current account
func getAWSImages(account string, client *ec2.EC2) ([]Image, error) {
	input := &ec2.DescribeImagesInput{
		Owners: aws.StringSlice([]string{awsOwnerIDSelfValue}),
	}
	awsImages, err := client.DescribeImages(input)
	if err != nil {
		return nil, err
	}
	result := []Image{}
	for _, ami := range awsImages.Images {
		ti, err := time.Parse(time.RFC3339, *ami.CreationDate)
		if err != nil {
			return nil, err
		}
		img := awsImage{baseImage{
			baseResource: baseResource{
				csp:          AWS,
				owner:        account,
				id:           *ami.ImageId,
				location:     *client.Config.Region,
				creationTime: ti,
				public:       *ami.Public,
				tags:         convertAWSTags(ami.Tags),
			},
			name: *ami.Name,
		}}
		for _, mapping := range ami.BlockDeviceMappings {
			if mapping.Ebs != nil {
				img.baseImage.sizeGB += *mapping.Ebs.VolumeSize
			}
		}
		result = append(result, &img)
	}
	return result, nil
}

// getAWSVolumes will get all volumes (both attached and un-attached)
// in the current account
func getAWSVolumes(account string, client *ec2.EC2) ([]Volume, error) {
	input := new(ec2.DescribeVolumesInput)
	awsVolumes, err := client.DescribeVolumes(input)
	if err != nil {
		return nil, err
	}
	result := []Volume{}
	for _, volume := range awsVolumes.Volumes {
		vol := awsVolume{baseVolume{
			baseResource: baseResource{
				csp:          AWS,
				owner:        account,
				id:           *volume.VolumeId,
				location:     *client.Config.Region,
				creationTime: *volume.CreateTime,
				public:       false,
				tags:         convertAWSTags(volume.Tags),
			},
			sizeGB:     *volume.Size,
			attached:   len(volume.Attachments) > 0,
			encrypted:  *volume.Encrypted,
			volumeType: *volume.VolumeType,
		}}
		result = append(result, &vol)
	}
	return result, nil
}

// getAWSSnapshots will get all snapshots in AWS owned
// by the current account
func getAWSSnapshots(account string, client *ec2.EC2) ([]Snapshot, error) {
	input := &ec2.DescribeSnapshotsInput{
		OwnerIds: aws.StringSlice([]string{awsOwnerIDSelfValue}),
	}
	awsSnapshots, err := client.DescribeSnapshots(input)
	if err != nil {
		return nil, err
	}
	result := []Snapshot{}
	for _, snapshot := range awsSnapshots.Snapshots {
		snap := awsSnapshot{baseSnapshot{
			baseResource: baseResource{
				csp:          AWS,
				owner:        account,
				id:           *snapshot.SnapshotId,
				location:     *client.Config.Region,
				creationTime: *snapshot.StartTime,
				public:       false,
				tags:         convertAWSTags(snapshot.Tags),
			},
			sizeGB:    *snapshot.VolumeSize,
			encrypted: *snapshot.Encrypted,
		}}
		result = append(result, &snap)
	}
	return result, nil
}

func getAllEC2Resources(accounts []string, funcToRun func(client *ec2.EC2, account string)) {
	sess := session.Must(session.NewSession())
	forEachAccount(accounts, sess, func(account string, cred *credentials.Credentials) {
		log.Println("Accessing account", account)
		forEachAWSRegion(func(region string) {
			client := ec2.New(sess, &aws.Config{
				Credentials: cred,
				Region:      aws.String(region),
			})
			funcToRun(client, account)
		})
	})
}

// forEachAccount is a higher order function that will, for
// every account, create credentials and call the specified
// function with those creds
func forEachAccount(accounts []string, sess *session.Session, funcToRun func(account string, cred *credentials.Credentials)) {
	var wg sync.WaitGroup
	for i := range accounts {
		wg.Add(1)
		go func(x int) {
			creds := stscreds.NewCredentials(sess, fmt.Sprintf(assumeRoleARNTemplate, accounts[x]))
			funcToRun(accounts[x], creds)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

// forEachAWSRegion is a higher order function that will, for
// every available AWS region, run the specified function
func forEachAWSRegion(funcToRun func(region string)) {
	regions, exists := endpoints.RegionsForService(endpoints.DefaultPartitions(), endpoints.AwsPartitionID, endpoints.Ec2ServiceID)
	if !exists {
		panic("The regions for EC2 in the standard partition should exist")
	}
	var wg sync.WaitGroup
	for regionID := range regions {
		wg.Add(1)
		go func(x string) {
			funcToRun(x)
			wg.Done()
		}(regionID)
	}
	wg.Wait()
}

func handleAWSAccessDenied(account string, err error) {
	// Cast err to awserr.Error to handle specific AWS errors
	aerr, ok := err.(awserr.Error)
	if ok && aerr.Code() == accessDeniedErrorCode {
		// The account does not have the role setup correctly
		log.Printf("The account '%s' denied access\n", account)
	} else if ok {
		// Some other AWS error occured
		log.Fatalln(aerr)
	} else {
		//Some other non-AWS error occured
		log.Fatalln(err)
	}
}

func convertAWSTags(tags []*ec2.Tag) map[string]string {
	result := make(map[string]string)
	for _, tag := range tags {
		result[*tag.Key] = *tag.Value
	}
	return result
}

func clientForAWSResource(res Resource) *ec2.EC2 {
	sess := session.Must(session.NewSession())
	creds := stscreds.NewCredentials(sess, fmt.Sprintf(assumeRoleARNTemplate, res.Owner()))
	return ec2.New(sess, &aws.Config{
		Credentials: creds,
		Region:      aws.String(res.Location()),
	})
}

func addAWSTag(r Resource, key, value string, overwrite bool) error {
	_, exist := r.Tags()[key]
	if exist && !overwrite {
		return fmt.Errorf("Key %s already exist on %s", key, r.ID())
	}
	client := clientForAWSResource(r)
	input := &ec2.CreateTagsInput{
		Resources: aws.StringSlice([]string{r.ID()}),
		Tags: []*ec2.Tag{&ec2.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		}},
	}
	_, err := client.CreateTags(input)
	return err
}