// Copyright (c) 2018 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/agaridata/cloudsweeper/cloud"
	"github.com/joho/godotenv"
)

const optionalDefault = "<optional>"

type lookup struct {
	confKey      string
	defaultValue string
}

var configMapping = map[string]lookup{
	// General variables
	"csp":      {"CS_CSP", "aws"},
	"org-file": {"CS_ORG_FILE", "organization.json"},

	// Billing related
	"billing-account":       {"CS_BILLING_ACCOUNT", ""},
	"billing-bucket-region": {"CS_BILLING_BUCKET_REGION", ""},
	"billing-csv-prefix":    {"CS_BILLING_CSV_PREFIX", ""},
	"billing-bucket":        {"CS_BILLING_BUCKET_NAME", ""},
	"billing-sort-tag":      {"CS_BILLING_SORT_TAG", optionalDefault},

	// Email variables
	"smtp-username": {"CS_SMTP_USER", ""},
	"smtp-password": {"CS_SMTP_PASSWORD", ""},
	"smtp-server":   {"CS_SMTP_SERVER", ""},
	"smtp-port":     {"CS_SMTP_PORT", "587"},

	// Notifying specific variables
	"warning-hours":            {"CS_WARNING_HOURS", "48"},
	"display-name":             {"CS_DISPLAY_NAME", "Cloudsweeper"},
	"mail-from":                {"CS_MAIL_FROM", ""},
	"billing-report-addressee": {"CS_BILLING_REPORT_ADDRESSEE", ""},
	"total-sum-addressee":      {"CS_TOTAL_SUM_ADDRESSEE", ""},
	"mail-domain":              {"CS_EMAIL_DOMAIN", ""},

	// Setup variables
	"aws-master-arn": {"CS_MASTER_ARN", ""},

	// Clean thresholds
	"clean-untagged-older-than-days":   {"CLEAN_UNTAGGED_OLDER_THAN_DAYS", "30"},
	"clean-instances-older-than-days":  {"CLEAN_INSTANCES_OLDER_THAN_DAYS", "182"},
	"clean-images-older-than-days":     {"CLEAN_IMAGES_OLDER_THAN_DAYS", "182"},
	"clean-snapshots-older-than-days":  {"CLEAN_SNAPSHOTS_OLDER_THAN_DAYS", "182"},
	"clean-unattached-older-than-days": {"CLEAN_UNATTACHED_OLDER_THAN_DAYS", "30"},
	"clean-bucket-not-modified-days":   {"CLEAN_BUCKET_NOT_MODIFIED_DAYS", "182"},
	"clean-bucket-older-than-days":     {"CLEAN_BUCKET_OLDER_THAN_DAYS", "7"},
	"clean-keep-n-component-images":    {"CLEAN_KEEP_N_COMPONENT_IMAGES", "2"},

	//  Notify thresholds
	"notify-untagged-older-than-days":   {"NOTIFY_UNTAGGED_OLDER_THAN_DAYS", "14"},
	"notify-instances-older-than-days":  {"NOTIFY_INSTANCES_OLDER_THAN_DAYS", "30"},
	"notify-images-older-than-days":     {"NOTIFY_IMAGES_OLDER_THAN_DAYS", "30"},
	"notify-unattached-older-than-days": {"NOTIFY_UNATTACHED_OLDER_THAN_DAYS", "30"},
	"notify-snapshots-older-than-days":  {"NOTIFY_SNAPSHOTS_OLDER_THAN_DAYS", "30"},
	"notify-buckets-older-than-days":    {"NOTIFY_BUCKETS_OLDER_THAN_DAYS", "30"},
	"notify-whitelist-older-than-days":  {"NOTIFY_WHITELIST_OLDER_THAN_DAYS", "182"},
	"notify-dnd-older-than-days":        {"NOTIFY_DND_OLDER_THAN_DAYS", "7"},

	"required-tags": {"REQUIRED_TAGS", optionalDefault},
}

func loadFile(fileName string) {
	var err error
	config, err = godotenv.Read(fileName)
	if err != nil {
		log.Fatalf("Could not load config file '%s': %s", fileName, err)
	}
}

func loadDoNotDelete() {
	if doNotDelete == nil {
		doNotDelete = make(map[string]bool)
	}
	dndFile, err := os.Open(doNotDeleteFileName)
	if err != nil {
		fmt.Println(err)
	}
	defer dndFile.Close()
	scanner := bufio.NewScanner(dndFile)
	for scanner.Scan() {
		doNotDelete[strings.Trim(scanner.Text(), " ")] = true
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}

func loadThresholds() {
	for _, v := range thnames {
		thresholds[v] = findConfigInt(v)
	}
}

func findConfig(name string) string {
	if _, exist := configMapping[name]; !exist {
		log.Fatalf("Unknown config option: %s", name)
	}
	flagVal := flag.Lookup(name).Value.String()
	if flagVal != "" {
		return flagVal
	} else if confVal, ok := config[configMapping[name].confKey]; ok && confVal != "" {
		maybeNoValExit(confVal, name)
		return confVal
	} else {
		defaultVal := configMapping[name].defaultValue
		if defaultVal == optionalDefault {
			return ""
		}
		maybeNoValExit(defaultVal, name)
		return defaultVal
	}
}

func maybeNoValExit(val, name string) {
	if val == "" {
		log.Fatalf("No value specified for --%s", name)
	}
}

func findConfigInt(name string) int {
	val := findConfig(name)
	i, err := strconv.Atoi(val)
	if err != nil {
		log.Fatalf("Value specified for %s is not an integer", name)
	}
	return i
}

func cspFromConfig(rawFlag string) cloud.CSP {
	flagVal := strings.ToLower(rawFlag)
	switch flagVal {
	case cspFlagAWS:
		return cloud.AWS
	case cspFlagGCP:
		return cloud.GCP
	default:
		fmt.Fprintf(os.Stderr, "Invalid CSP flag \"%s\" specified\n", rawFlag)
		os.Exit(1)
		return cloud.AWS
	}
}

func tagsFromConfig(rawFlag string) []string {
	tags := strings.Split(rawFlag, ",")
	for _, tag := range tags {
		if len(tag) == 0 {
			log.Println("Empty tag detected from config")
			return []string{}
		}
	}
	return tags
}
