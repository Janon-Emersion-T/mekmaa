package main

import "time"

const (
	displayDateLayout     = "02-Jan-2006"
	displayDateTimeLayout = "02-Jan-2006 3:04 PM"
)

var sriLankaLocation = loadSriLankaLocation()

func init() {
	time.Local = sriLankaLocation
}

func loadSriLankaLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Colombo")
	if err == nil {
		return location
	}
	return time.FixedZone("Asia/Colombo", 5*60*60+30*60)
}

func currentBusinessTime() time.Time {
	return time.Now().In(sriLankaLocation)
}

func currentBusinessDate() string {
	return currentBusinessTime().Format("2006-01-02")
}
