package main

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/frizinak/harvest-timetracking/forecast"
	"github.com/frizinak/harvest-timetracking/harvest"
)

const dateFormat = "2006-01-02"

type Config struct {
	AccountID         string   `json:"account_id"`
	ForecastAccountID string   `json:"forecast_account_id"`
	Token             string   `json:"token"`
	WeekdaysOff       []string `json:"weekdays_off"`
	ExcludedDates     []string `json:"exclude_dates"`
	excludedMap       map[string]struct{}
	weekdaysOffMap    map[time.Weekday]struct{}
}

func (c *Config) Validate() error {
	c.excludedMap = make(map[string]struct{})
	for _, v := range c.ExcludedDates {
		c.excludedMap[v] = struct{}{}
		_, err := time.Parse(dateFormat, v)
		if err != nil {
			return err
		}
	}

	c.weekdaysOffMap = make(map[time.Weekday]struct{})
	wds := map[string]time.Weekday{
		strings.ToLower(time.Monday.String()):    time.Monday,
		strings.ToLower(time.Tuesday.String()):   time.Tuesday,
		strings.ToLower(time.Wednesday.String()): time.Wednesday,
		strings.ToLower(time.Thursday.String()):  time.Thursday,
		strings.ToLower(time.Friday.String()):    time.Friday,
		strings.ToLower(time.Saturday.String()):  time.Saturday,
		strings.ToLower(time.Sunday.String()):    time.Sunday,
	}
	for _, v := range c.WeekdaysOff {
		wd, ok := wds[strings.ToLower(v)]
		if !ok {
			return fmt.Errorf("Invalid weekday '%s'", v)
		}

		c.weekdaysOffMap[wd] = struct{}{}
	}

	if len(c.weekdaysOffMap) > 6 {
		return errors.New("What are you using this program for, if you take every day off?")
	}

	return nil
}

func (c *Config) Excluded(t time.Time) bool {
	_, ok := c.excludedMap[t.Format(dateFormat)]
	return ok
}

func (c *Config) Off(t time.Time) bool {
	_, ok := c.weekdaysOffMap[t.Weekday()]
	return ok
}

func (c *Config) AmountOff() int {
	return len(c.weekdaysOffMap)
}

func (c *Config) WorkWeek() int {
	return 7 - len(c.weekdaysOffMap)
}

type Timetracking struct {
	l            *log.Logger
	conf         *Config
	harvest      *harvest.Harvest
	forecast     *forecast.Forecast
	user         *harvest.User
	forecastUser *forecast.User
}

func New(l *log.Logger, c *Config) (*Timetracking, error) {
	aid, err := strconv.Atoi(c.AccountID)
	if err != nil {
		return nil, errors.New("account_id should be a numeric value")
	}

	fid := 0
	if c.ForecastAccountID != "" {
		fid, err = strconv.Atoi(c.ForecastAccountID)
		if err != nil {
			return nil, errors.New("forecast_account_id should be a numeric value or empty")
		}
	}

	return &Timetracking{
		l:        l,
		conf:     c,
		harvest:  harvest.New(aid, c.Token),
		forecast: forecast.New(fid, c.Token),
	}, nil
}

func (t *Timetracking) SetUID(uid int) (err error) {
	t.user = nil
	if uid == 0 {
		t.user, err = t.harvest.GetMe()
		return
	}

	t.user, err = t.harvest.GetUser(uid)
	return
}

func (t *Timetracking) SetForecastUID(uid int) (err error) {
	t.forecastUser = nil
	var me *forecast.Me
	if uid == 0 {
		me, err = t.forecast.GetMe()
		if err != nil {
			return
		}
		uid = me.ID
	}

	t.forecastUser, err = t.forecast.GetUser(uid)
	return
}

func (t *Timetracking) User() *harvest.User {
	return t.user
}

func (t *Timetracking) ForecastUser() *forecast.User {
	return t.forecastUser
}

func (t *Timetracking) GetRecentDaysGrouped(
	amount int,
	from time.Time,
	actualDays bool,
	groupBy string,
) (int, harvest.Grouped, error) {
	groupFormat := "2006-01-02"
	switch groupBy {
	case groupByDay:
	case groupByWeek:
	case groupByMonth:
		groupFormat = "2006-01"
	case groupByYear:
		groupFormat = "2006"
	default:
		return 0, nil, fmt.Errorf("Invalid group '%s'", groupBy)
	}

	days, entries, err := t.GetRecentDays(amount, from, actualDays)
	if err != nil {
		return 0, nil, err
	}

	group := entries.Group(
		func(e *harvest.TimeEntry) (string, bool) {
			if e.SpentDate == nil {
				return "", false
			}

			d := e.SpentDate.Time
			for t.conf.Excluded(d) || t.conf.Off(d) {
				d = d.AddDate(0, 0, -1)
			}
			e.SpentDate = &harvest.Date{d}

			if groupBy == groupByWeek {
				y, w := e.SpentDate.ISOWeek()
				return fmt.Sprintf("%d|%d", y, w), true
			}

			return e.SpentDate.Format(groupFormat), true
		},
	)

	return days, group, err
}

func (t *Timetracking) GetRecentDays(
	amount int,
	from time.Time,
	actualDays bool,
) (int, harvest.TimeEntries, error) {
	params := &harvest.TimeEntriesParams{UserID: &t.User().ID, To: &from}

	entries := make(harvest.TimeEntries, 0, amount)
	counter := make(map[string]struct{})

	if actualDays {
		d := from
		for {
			for t.conf.Excluded(d) || t.conf.Off(d) {
				d = d.AddDate(0, 0, -1)
			}

			counter[d.Format(dateFormat)] = struct{}{}
			entries = append(
				entries,
				&harvest.TimeEntry{
					Hours:     harvest.DurationHours{0},
					SpentDate: &harvest.Date{d},
				},
			)
			d = d.AddDate(0, 0, -1)
			if len(counter) == amount {
				break
			}
		}
	}

outer:
	for {
		res, err := t.harvest.GetTimeEntries(params)
		if err != nil {
			return 0, nil, err
		}

		for _, e := range res.TimeEntries {
			if e.SpentDate == nil {
				continue
			}

			d := e.SpentDate.Time
			for t.conf.Excluded(d) || t.conf.Off(d) {
				d = d.AddDate(0, 0, -1)
			}

			df := d.Format(dateFormat)
			if _, ok := counter[df]; !ok && len(counter) == amount {
				break outer
			}
			counter[df] = struct{}{}
			entries = append(entries, e)
		}

		if res.NextPage == nil {
			break
		}

		params.Page = res.NextPage
	}

	return len(counter), entries, nil
}

func (t *Timetracking) GetAssignmentsByName(projectName string) ([]*forecast.Assignment, error) {
	if t.forecastUser == nil || t.forecastUser.ID == 0 {
		return nil, errors.New("No forecast user set")
	}

	ps, err := t.forecast.GetProjects()
	if err != nil {
		return nil, err
	}

	id := 0
	for _, p := range ps.Projects {
		if p.Name == projectName {
			id = p.ID
		}
	}

	if id == 0 {
		return nil, fmt.Errorf("Could not find project id for a project named '%s'", projectName)
	}

	as, err := t.forecast.GetAssignments(
		&forecast.AssignmentsParams{
			ProjectID: &id,
			PersonID:  &t.forecastUser.ID,
		},
	)
	if err != nil {
		return nil, err
	}

	return as.Assignments, nil
}
