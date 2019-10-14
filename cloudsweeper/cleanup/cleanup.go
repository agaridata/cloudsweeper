// Copyright (c) 2018 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: BSD-2-Clause

package cleanup

import (
	"log"
	"sort"
	"time"

	"github.com/agaridata/cloudsweeper/cloud"
	"github.com/agaridata/cloudsweeper/cloud/billing"
	"github.com/agaridata/cloudsweeper/cloud/filter"
)

const (
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

		getThreshold := func(key string, thresholds map[string]int) int {
			threshold, found := thresholds[key]
			if found {
				return threshold
			} else {
				log.Fatalf("Threshold '%s' not found", key)
				return 99999
			}
		}

		// Deletion thresholds
		timeToDeleteGeneral := time.Now().AddDate(0, 0, 4)
		timeToDeleteUnnamedInstances := time.Now().AddDate(0, 0, 1)

		resourcesToTag := cloud.AllResourceCollection{}
		resourcesToTag.Owner = owner
		// Store a separate list of all resources since I couldn't for the life of me figure out how to
		// pass a []Image to a function that takes []Resource without explicitly converting everything...
		tagListGeneral := []cloud.Resource{}
		tagListUnnamedInstances := []cloud.Resource{}
		totalCost := 0.0

		// General filters
		untaggedFilter := filter.New()
		untaggedFilter.AddGeneralRule(filter.IsUntaggedWithException("Name"))
		untaggedFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-untagged-older-than-days", thresholds)))
		untaggedFilter.AddSnapshotRule(filter.IsNotInUse())
		untaggedFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))
		untaggedFilter.AddVolumeRule(filter.IsUnattached())

		// INSTANCES
		instanceFilter := filter.New()
		instanceFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-instances-older-than-days", thresholds)))
		instanceFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		noNameFilter := filter.New()
		noNameFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-untagged-older-than-days", thresholds))) // TODO: Remove?
		noNameFilter.AddGeneralRule(filter.IsUntaggedWithException("Name"))
		noNameFilter.AddGeneralRule(filter.Negate(filter.HasTag("Name")))
		noNameFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		// Helper map to avoid duplicated images
		alreadySelectedInstances := map[string]bool{}

		// Unnamed instances (without tags)
		for _, res := range filter.Instances(res.Instances, noNameFilter) {
			resourcesToTag.Instances = append(resourcesToTag.Instances, res)
			tagListUnnamedInstances = append(tagListUnnamedInstances, res)
			alreadySelectedInstances[res.ID()] = true
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// General case
		for _, res := range filter.Instances(res.Instances, instanceFilter, untaggedFilter) {
			resourcesToTag.Instances = append(resourcesToTag.Instances, res)
			tagListGeneral = append(tagListGeneral, res)
			alreadySelectedInstances[res.ID()] = true
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// VOLUMES
		volumeFilter := filter.New()
		volumeFilter.AddVolumeRule(filter.IsUnattached())
		volumeFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-unattached-older-than-days", thresholds)))
		volumeFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		for _, res := range filter.Volumes(res.Volumes, volumeFilter, untaggedFilter) {
			resourcesToTag.Volumes = append(resourcesToTag.Volumes, res)
			tagListGeneral = append(tagListGeneral, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// SNAPSHOTS
		snapshotFilter := filter.New()
		snapshotFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-snapshots-older-than-days", thresholds)))
		snapshotFilter.AddSnapshotRule(filter.IsNotInUse())
		snapshotFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		for _, res := range filter.Snapshots(res.Snapshots, snapshotFilter, untaggedFilter) {
			resourcesToTag.Snapshots = append(resourcesToTag.Snapshots, res)
			tagListGeneral = append(tagListGeneral, res)
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// BUCKETS
		bucketFilter := filter.New()
		bucketFilter.AddBucketRule(filter.NotModifiedInXDays(getThreshold("clean-bucket-not-modified-days", thresholds)))
		bucketFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-bucket-older-than-days", thresholds)))
		bucketFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))

		if buck, ok := allBuckets[owner]; ok {
			for _, res := range filter.Buckets(buck, bucketFilter, untaggedFilter) {
				resourcesToTag.Buckets = append(resourcesToTag.Buckets, res)
				tagListGeneral = append(tagListGeneral, res)
				totalCost += billing.BucketPricePerMonth(res)
			}
		}

		// IMAGES
		unformattedImageFilter := filter.New()
		unformattedImageFilter.AddGeneralRule(filter.OlderThanXDays(getThreshold("clean-images-older-than-days", thresholds)))
		unformattedImageFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))
		unformattedImageFilter.AddImageRule(filter.DoesNotFollowFormat())

		formattedImageFilter := filter.New()
		formattedImageFilter.AddGeneralRule(filter.Negate(filter.TaggedForCleanup()))
		formattedImageFilter.AddImageRule(filter.FollowsFormat())

		// Helper map to avoid duplicated images
		alreadySelectedImages := map[string]bool{}

		// Untagged images
		for _, res := range filter.Images(res.Images, untaggedFilter) {
			resourcesToTag.Images = append(resourcesToTag.Images, res)
			tagListGeneral = append(tagListGeneral, res)
			alreadySelectedImages[res.ID()] = true
			days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
			costPerDay := billing.ResourceCostPerDay(res)
			totalCost += days * costPerDay
		}

		// Images NOT following the component-date pattern
		for _, res := range filter.Images(res.Images, unformattedImageFilter) {
			if _, found := alreadySelectedImages[res.ID()]; !found {
				resourcesToTag.Images = append(resourcesToTag.Images, res)
				tagListGeneral = append(tagListGeneral, res)
				alreadySelectedImages[res.ID()] = true
				days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
				costPerDay := billing.ResourceCostPerDay(res)
				totalCost += days * costPerDay
			}
		}

		// Images following the component-date pattern
		formattedImages := getAllButNLatestComponents(res.Images, getThreshold("clean-keep-n-component-images", thresholds))
		for _, res := range filter.Images(formattedImages, formattedImageFilter) {
			if _, found := alreadySelectedImages[res.ID()]; !found {
				resourcesToTag.Images = append(resourcesToTag.Images, res)
				tagListGeneral = append(tagListGeneral, res)
				alreadySelectedImages[res.ID()] = true
				days := time.Now().Sub(res.CreationTime()).Hours() / 24.0
				costPerDay := billing.ResourceCostPerDay(res)
				totalCost += days * costPerDay
			}
		}

		log.Printf("%s: Attempting to apply tags to resources")
		applyTags(tagListGeneral, timeToDeleteGeneral, totalCost, dryRun)
		applyTags(tagListUnnamedInstances, timeToDeleteUnnamedInstances, totalCost, dryRun)

		allResourcesToTag[owner] = &resourcesToTag
	}
	return allResourcesToTag
}

func applyTags(resources []cloud.Resource, timeToDelete Time, totalCost float, dryRun bool) {
	if dryRun {
		log.Printf("Resources not tagged since this is a dry run")
	} else if totalCost < totalCostThreshold {
		log.Printf("Resources not tagged since the total cost $%.2f is less than $%.2f", totalCost, totalCostThreshold)
	} else {
		for _, res := range resources {
			err := res.SetTag(filter.DeleteTagKey, timeToDelete.Format(time.RFC3339), true)
			if err != nil {
				log.Printf("Failed to tag %s for deletion: %s\n", res.ID(), err)
			} else {
				log.Printf("Marked %s for deletion at %s\n", res.ID(), timeToDelete)
			}
		}
	}
}

// GetAllButNLatestComponents will look at AMIs, and return all but the two latest for each
// component, where the naming of the AMIs is on the form:
//		"<component name>-<creation timestamp>"
func getAllButNLatestComponents(images []cloud.Image, componentsToKeep int) []cloud.Image {
	resourcesToTag := []cloud.Image{}
	componentDatesMap := map[string][]time.Time{}

	for _, image := range images {
		componentName, creationDate := filter.ParseFormat(image)
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

		sort.Slice(times, func(i, j int) bool {
			// Sort times so that newest are first
			return times[i].After(times[j])
		})

		minimumIndex := componentsToKeep
		if minimumIndex > len(times) {
			minimumIndex = len(times)
		}
		threshold := times[minimumIndex-1]
		return threshold
	}

	for _, image := range images {
		componentName, creationDate := filter.ParseFormat(image)
		threshold := findThreshold(componentName)
		if creationDate.Before(threshold) {
			// This AMI is too old, mark it
			resourcesToTag = append(resourcesToTag, image)
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
