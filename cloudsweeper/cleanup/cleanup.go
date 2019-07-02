// Copyright (c) 2018 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: BSD-2-Clause

package cleanup

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cloudtools/cloudsweeper/cloud"
	"github.com/cloudtools/cloudsweeper/cloud/billing"
	"github.com/cloudtools/cloudsweeper/cloud/filter"
)

const (
	releaseTag         = "Release"
	totalCostThreshold = 10.0
)

// MarkForCleanup will look for resources that should be automatically
// cleaned up. These resources are not deleted directly, but are given
// a tag that will delete the resources 4 days from now. The rules
// for marking a resource for cleanup are the following:
// 		- unattached volumes > 30 days old
//		- unused/unaccessed buckets > 6 months (182 days)
// 		- non-whitelisted AMIs > 6 months
// 		- non-whitelisted snapshots > 6 months
// 		- non-whitelisted volumes > 6 months
//		- untagged resources > 30 days (this should take care of instances)
func MarkForCleanup(mngr cloud.ResourceManager, thresholds map[string]int, dryRun bool) map[string]*cloud.AllResourceCollection {
	allResources := mngr.AllResourcesPerAccount()
	allBuckets := mngr.BucketsPerAccount()
	allResourcesToTag := make(map[string]*cloud.AllResourceCollection)

	for owner, res := range allResources {
		log.Println("Marking resources for cleanup in", owner)
		untaggedFilter := filter.New()
		untaggedFilter.AddGeneralRule(filter.IsUntaggedWithException("Name"))
		untaggedFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-untagged-older-than-days"]))
		untaggedFilter.AddSnapshotRule(filter.IsNotInUse())
		untaggedFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		instanceFilter := filter.New()
		instanceFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-instances-older-than-days"]))
		instanceFilter.AddGeneralRule(filter.Negate(filter.HasTag(releaseTag)))
		instanceFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		snapshotFilter := filter.New()
		instanceFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-snapshots-older-than-days"]))
		snapshotFilter.AddSnapshotRule(filter.IsNotInUse())
		snapshotFilter.AddGeneralRule(filter.Negate(filter.HasTag(releaseTag)))
		snapshotFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		imageFilter := filter.New()
		imageFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-images-older-than-days"]))
		imageFilter.AddGeneralRule(filter.Negate(filter.HasTag(releaseTag)))
		imageFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		volumeFilter := filter.New()
		volumeFilter.AddVolumeRule(filter.IsUnattached())
		volumeFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-unattatched-older-than-days"]))
		volumeFilter.AddGeneralRule(filter.Negate(filter.HasTag(releaseTag)))
		volumeFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		bucketFilter := filter.New()
		bucketFilter.AddBucketRule(filter.NotModifiedInXDays(thresholds["clean-bucket-not-modified-days"]))
		bucketFilter.AddGeneralRule(filter.OlderThanXDays(thresholds["clean-bucket-older-than-days"]))
		bucketFilter.AddGeneralRule(filter.Negate(filter.HasTag(releaseTag)))
		bucketFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		timeToDelete := time.Now().AddDate(0, 0, 4)

		resourcesToTag := cloud.AllResourceCollection{}
		resourcesToTag.Owner = owner
		// Store a separate list of all resources since I couldn't for the life of me figure out how to
		// pass a []Image to a function that takes []Resource without explicitly converting everything...
		tagList := []cloud.Resource{}
		totalCost := 0.0

		// Tag instances
		for _, res := range filter.Instances(res.Instances, instanceFilter, untaggedFilter) {
			resourcesToTag.Instances = append(resourcesToTag.Instances, res)
			tagList = append(tagList, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// Tag volumes
		for _, res := range filter.Volumes(res.Volumes, volumeFilter) {
			resourcesToTag.Volumes = append(resourcesToTag.Volumes, res)
			tagList = append(tagList, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// Tag snapshots
		for _, res := range filter.Snapshots(res.Snapshots, snapshotFilter, untaggedFilter) {
			resourcesToTag.Snapshots = append(resourcesToTag.Snapshots, res)
			tagList = append(tagList, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// Tag images
		for _, res := range filter.Images(res.Images, imageFilter, untaggedFilter) {
			resourcesToTag.Images = append(resourcesToTag.Images, res)
			tagList = append(tagList, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// Tag buckets
		if buck, ok := allBuckets[owner]; ok {
			for _, res := range filter.Buckets(buck, bucketFilter, untaggedFilter) {
				resourcesToTag.Buckets = append(resourcesToTag.Buckets, res)
				tagList = append(tagList, res)
				totalCost += billing.BucketPricePerMonth(res)
			}
		}

		// Tag old autogenerated images using the component-date pattern
		componentImagesToTag := getAllButNLatestComponents(res.Images, thresholds["clean-keep-n-component-images"])
		resourcesToTag.Images = append(resourcesToTag.Images, componentImagesToTag...)

		if dryRun {
			log.Printf("Not tagging resources since this is a dry run")
		} else if totalCost < totalCostThreshold {
			log.Printf("%s: Skipping the tagging of resources, total cost $%.2f is less than $%.2f", owner, totalCost, totalCostThreshold)
		} else {
			for _, res := range tagList {
				err := res.SetTag(filter.DeleteTagKey, timeToDelete.Format(time.RFC3339), true)
				if err != nil {
					log.Printf("%s: Failed to tag %s for deletion: %s\n", owner, res.ID(), err)
				} else {
					log.Printf("%s: Marked %s for deletion at %s\n", owner, res.ID(), timeToDelete)
				}
			}
		}
		allResourcesToTag[owner] = &resourcesToTag
	}
	return allResourcesToTag
}

// GetAllButNLatestComponents will look at AMIs, and return all but the two latest for each
// component, where the naming of the AMIs is on the form:
//		"<component name>-<creation timestamp>"
func getAllButNLatestComponents(images []cloud.Image, componentsToKeep int) []cloud.Image {
	resourcesToTag := []cloud.Image{}
	componentDatesMap := map[string][]time.Time{}

	splitNameAndTime := func(ami cloud.Image) (name string, creationTime time.Time, err error) {
		nameParts := strings.Split(ami.Name(), "-")
		if len(nameParts) < 2 {
			log.Printf("AMI %s doesn't follow the <component>-<time> format", ami.ID())
			return "", time.Time{}, errors.New("AMI doesn't follow the correct format")
		}
		rawDate := nameParts[len(nameParts)-1]
		componentName := strings.Join(nameParts[:len(nameParts)-1], "-")
		const format = "20060102150405"
		if parsedDate, err := time.Parse(format, rawDate); err == nil {
			return componentName, parsedDate, nil
		}
		log.Printf("Could not parse time \"%s\" of AMI %s", rawDate, ami.ID())
		return "", time.Time{}, errors.New("could not parse creation time of AMI")
	}

	for _, ami := range images {
		componentName, creationDate, err := splitNameAndTime(ami)
		if err != nil {
			fmt.Printf("Got error for AMI %s: %v", ami.ID(), err)
			continue
		}
		if _, found := componentDatesMap[componentName]; !found {
			componentDatesMap[componentName] = []time.Time{}
		}
		componentDatesMap[componentName] = append(componentDatesMap[componentName], creationDate)
	}

	findThreshold := func(componentName string) time.Time {
		times, found := componentDatesMap[componentName]
		if !found {
			log.Fatalln("Times not found for some reason")
			return time.Now().AddDate(-10, 0, 0)
		}
		if componentsToKeep > len(times) {
			componentsToKeep = len(times)
		}

		sort.Slice(times, func(i, j int) bool {
			// Sort times so that newest are first
			return times[i].After(times[j])
		})

		threshold := times[componentsToKeep-1]
		return threshold
	}

	for _, ami := range images {
		componentName, creationDate, err := splitNameAndTime(ami)
		if err != nil {
			log.Printf("Got error for AMI %s: %v", ami.ID(), err)
			continue
		}
		threshold := findThreshold(componentName)
		if creationDate.Before(threshold) {
			// This AMI is too old, mark it
			resourcesToTag = append(resourcesToTag, ami)
		}
	}
	return resourcesToTag
}

// PerformCleanup will run different cleanup functions which all
// do some sort of rule based cleanup
func PerformCleanup(mngr cloud.ResourceManager) {
	// Cleanup all resources with a lifetime tag that has passed. This
	// includes both the lifetime and the expiry tag
	cleanupLifetimePassed(mngr)
}

func cleanupLifetimePassed(mngr cloud.ResourceManager) {
	allResources := mngr.AllResourcesPerAccount()
	allBuckets := mngr.BucketsPerAccount()
	for owner, resources := range allResources {
		log.Println("Performing lifetime check in", owner)
		lifetimeFilter := filter.New()
		lifetimeFilter.AddGeneralRule(filter.LifetimeExceeded())

		expiryFilter := filter.New()
		expiryFilter.AddGeneralRule(filter.ExpiryDatePassed())

		deleteAtFilter := filter.New()
		deleteAtFilter.AddGeneralRule(filter.DeleteAtPassed())

		err := mngr.CleanupInstances(filter.Instances(resources.Instances, lifetimeFilter, expiryFilter, deleteAtFilter))
		if err != nil {
			log.Printf("Could not cleanup instances in %s, err:\n%s", owner, err)
		}
		err = mngr.CleanupImages(filter.Images(resources.Images, lifetimeFilter, expiryFilter, deleteAtFilter))
		if err != nil {
			log.Printf("Could not cleanup images in %s, err:\n%s", owner, err)
		}
		err = mngr.CleanupVolumes(filter.Volumes(resources.Volumes, lifetimeFilter, expiryFilter, deleteAtFilter))
		if err != nil {
			log.Printf("Could not cleanup volumes in %s, err:\n%s", owner, err)
		}
		err = mngr.CleanupSnapshots(filter.Snapshots(resources.Snapshots, lifetimeFilter, expiryFilter, deleteAtFilter))
		if err != nil {
			log.Printf("Could not cleanup snapshots in %s, err:\n%s", owner, err)
		}
		if bucks, ok := allBuckets[owner]; ok {
			err = mngr.CleanupBuckets(filter.Buckets(bucks, lifetimeFilter, expiryFilter, deleteAtFilter))
			if err != nil {
				log.Printf("Could not cleanup buckets in %s, err:\n%s", owner, err)
			}
		}
	}
}

// ResetCloudsweeper will remove any cleanup tags existing in the accounts
// associated with the provided resource manager
func ResetCloudsweeper(mngr cloud.ResourceManager) {
	allResources := mngr.AllResourcesPerAccount()
	allBuckets := mngr.BucketsPerAccount()

	for owner, res := range allResources {
		log.Println("Resetting Cloudsweeper tags in", owner)
		taggedFilter := filter.New()
		taggedFilter.AddGeneralRule(filter.HasTag(filter.DeleteTagKey))

		handleError := func(res cloud.Resource, err error) {
			if err != nil {
				log.Printf("Failed to remove tag on %s: %s\n", res.ID(), err)
			} else {
				log.Printf("Removed cleanup tag on %s\n", res.ID())
			}
		}

		// Un-Tag instances
		for _, res := range filter.Instances(res.Instances, taggedFilter) {
			handleError(res, res.RemoveTag(filter.DeleteTagKey))
		}

		// Un-Tag volumes
		for _, res := range filter.Volumes(res.Volumes, taggedFilter) {
			handleError(res, res.RemoveTag(filter.DeleteTagKey))
		}

		// Un-Tag snapshots
		for _, res := range filter.Snapshots(res.Snapshots, taggedFilter) {
			handleError(res, res.RemoveTag(filter.DeleteTagKey))
		}

		// Un-Tag images
		for _, res := range filter.Images(res.Images, taggedFilter) {
			handleError(res, res.RemoveTag(filter.DeleteTagKey))
		}

		// Un-Tag buckets
		if buck, ok := allBuckets[owner]; ok {
			for _, res := range filter.Buckets(buck, taggedFilter) {
				handleError(res, res.RemoveTag(filter.DeleteTagKey))
			}
		}

	}
}
