package res

import (
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

type awsResourceManager struct {
	accounts []string
}

type awsInstance struct {
	baseInstance
}

type awsImage struct {
	baseImage
}

const (
	assumeRoleARNTemplate = "arn:aws:iam::%s:role/brkt-HouseKeeper"

	accessDeniedErrorCode = "AccessDenied"
)

var (
	instanceStateFilterName = "instance-state-name"
	instanceStateRunning    = ec2.InstanceStateNameRunning

	imageOwnerIDSelfValue = "self"
)

func (m *awsResourceManager) InstancesPerAccount() map[string][]Instance {
	sess := session.Must(session.NewSession())
	resultMap := make(map[string][]Instance)
	m.forEachAccount(sess, func(account string, cred *credentials.Credentials) {
		log.Println("Getting instances for account", account)
		forEachAWSRegion(func(region string) {
			client := ec2.New(sess, &aws.Config{
				Credentials: cred,
				Region:      aws.String(region),
			})
			instances, err := getAWSInstances(client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			} else if len(instances) > 0 {
				resultMap[account] = append(resultMap[account], instances...)
			}
		})
	})
	return resultMap
}

func (m *awsResourceManager) ImagesPerAccount() map[string][]Image {
	sess := session.Must(session.NewSession())
	resultMap := make(map[string][]Image)
	m.forEachAccount(sess, func(account string, cred *credentials.Credentials) {
		log.Println("Getting images for account", account)
		forEachAWSRegion(func(region string) {
			client := ec2.New(sess, &aws.Config{
				Credentials: cred,
				Region:      aws.String(region),
			})
			images, err := getAWSImages(client)
			if err != nil {
				handleAWSAccessDenied(account, err)
			} else if len(images) > 0 {
				resultMap[account] = append(resultMap[account], images...)
			}
		})
	})
	return resultMap
}

// forEachAccount is a higher order function that will, for
// every account, create credentials and call the specified
// function with those creds
func (m *awsResourceManager) forEachAccount(sess *session.Session, funcToRun func(account string, cred *credentials.Credentials)) {
	var wg sync.WaitGroup
	for i := range m.accounts {
		wg.Add(1)
		go func(x int) {
			creds := stscreds.NewCredentials(sess, fmt.Sprintf(assumeRoleARNTemplate, m.accounts[x]))
			funcToRun(m.accounts[x], creds)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

// getAWSInstances will get all running instances using an already
// set-up client for a specific credential and region.
func getAWSInstances(client *ec2.EC2) ([]Instance, error) {
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
				id:           *instance.InstanceId,
				location:     *client.Config.Region,
				launchTime:   *instance.LaunchTime,
				public:       instance.PublicIpAddress != nil,
				tags:         make(map[string]string),
				instanceType: *instance.InstanceType,
			}}
			for _, tag := range instance.Tags {
				inst.tags[*tag.Key] = *tag.Value
			}
			result = append(result, &inst)
		}
	}
	return result, nil
}

// getAWSImages will get all AMIs owned by the current account
func getAWSImages(client *ec2.EC2) ([]Image, error) {
	input := &ec2.DescribeImagesInput{
		Owners: aws.StringSlice([]string{imageOwnerIDSelfValue}),
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
			id:           *ami.ImageId,
			location:     *client.Config.Region,
			creationTime: ti,
			public:       *ami.Public,
			tags:         make(map[string]string),
			name:         *ami.Name,
		}}
		for _, tag := range ami.Tags {
			img.tags[*tag.Key] = *tag.Value
		}
		result = append(result, &img)
	}
	return result, nil
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