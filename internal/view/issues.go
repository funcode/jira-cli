package view

import (
	"fmt"
	"io"
	"os"
	"strings"
	"encoding/json"
	"text/tabwriter"

	"github.com/ankitpokhrel/jira-cli/api"
	"github.com/ankitpokhrel/jira-cli/pkg/jira"
	"github.com/ankitpokhrel/jira-cli/pkg/jira/filter/issue"
	"github.com/ankitpokhrel/jira-cli/pkg/tui"
)

// DisplayFormat is a issue display type.
type DisplayFormat struct {
	Plain        bool
	NoHeaders    bool
	NoTruncate   bool
	Columns      []string
	FixedColumns uint
	TableStyle   tui.TableStyle
}

// IssueList is a list view for issues.
type IssueList struct {
	Total      int
	Project    string
	Server     string
	Data       []*jira.Issue
	Display    DisplayFormat
	Refresh    tui.RefreshFunc
	FooterText string
}

// Render renders the view.
func (l *IssueList) Render() error {
	if l.Display.Plain || tui.IsDumbTerminal() || tui.IsNotTTY() {
		w := tabwriter.NewWriter(os.Stdout, 0, tabWidth, 1, '\t', 0)
		return l.renderPlain(w)
	}

	renderer, err := MDRenderer()
	if err != nil {
		return err
	}

	data := l.data()
	if l.FooterText == "" {
		l.FooterText = fmt.Sprintf("Showing %d of %d results for project %q", len(data)-1, l.Total, l.Project)
	}

	view := tui.NewTable(
		tui.WithTableStyle(l.Display.TableStyle),
		tui.WithTableFooterText(l.FooterText),
		tui.WithTableHelpText(tableHelpText),
		tui.WithSelectedFunc(navigate(l.Server)),
		tui.WithViewModeFunc(func(r, c int, _ interface{}) (func() interface{}, func(interface{}) (string, error)) {
			dataFn := func() interface{} {
				ci := data.GetIndex(fieldKey)
				iss, _ := api.ProxyGetIssue(api.DefaultClient(false), data.Get(r, ci), issue.NewNumCommentsFilter(1))
				return iss
			}
			renderFn := func(i interface{}) (string, error) {
				iss := Issue{
					Server:  l.Server,
					Data:    i.(*jira.Issue),
					Options: IssueOption{NumComments: 1},
				}
				return iss.RenderedOut(renderer)
			}
			return dataFn, renderFn
		}),
		tui.WithCopyFunc(copyURL(l.Server)),
		tui.WithCopyKeyFunc(copyKey()),
		tui.WithMoveFunc(func(r, c int) func() (string, []string, tui.MoveHandlerFunc, string, tui.RefreshTableStateFunc) {
			dataFn := func() (string, []string, tui.MoveHandlerFunc, string, tui.RefreshTableStateFunc) {
				key := data[r][data.GetIndex(fieldKey)]
				client := api.DefaultClient(false)
				transitions, _ := api.ProxyTransitions(client, key)

				var actions []string
				for _, t := range transitions {
					actions = append(actions, t.Name)
				}

				actionHandler := func(state string) error {
					var tr *jira.Transition
					for _, t := range transitions {
						if strings.EqualFold(t.Name, state) {
							tr = t
							break
						}
					}
					if tr == nil {
						return fmt.Errorf("transition '%s' not found", state)
					}
					_, err := client.Transition(key, &jira.TransitionRequest{
						Transition: &jira.TransitionRequestData{
							ID:   tr.ID.String(),
							Name: tr.Name,
						},
					})
					return err
				}

				statusFieldIdx := data.GetIndex(fieldStatus)
				currentStatus := data.Get(r, statusFieldIdx)

				return key, actions, actionHandler, currentStatus, func(r, c int, val string) {
					data.Update(r, statusFieldIdx, val)
				}
			}
			return dataFn
		}),
		tui.WithRefreshFunc(l.Refresh),
		tui.WithFixedColumns(l.Display.FixedColumns),
	)

	return view.Paint(data)
}

// renderPlain renders the issue in plain view.
func (l *IssueList) renderPlain(w io.Writer) error {
	return renderPlain(w, l.data())
}

func (*IssueList) validColumnsMap() map[string]struct{} {
	columns := ValidIssueColumns()
	out := make(map[string]struct{}, len(columns))

	for _, c := range columns {
		out[c] = struct{}{}
	}

	return out
}

func (l *IssueList) header() []string {
	if len(l.Display.Columns) == 0 {
		validColumns := ValidIssueColumns()
		if l.Display.NoTruncate || !l.Display.Plain {
			return validColumns
		}
		return validColumns[0:4]
	}

	var (
		headers   []string
		hasKeyCol bool
	)

	columnsMap := l.validColumnsMap()
	for _, c := range l.Display.Columns {
		c = strings.ToUpper(c)
		if _, ok := columnsMap[c]; ok {
			headers = append(headers, strings.ToUpper(c))
		}
		if c == fieldKey {
			hasKeyCol = true
		}
	}

	// Key field is required in TUI to fetch relevant data later.
	// So, we will prepend the field if it is not available.
	if !hasKeyCol {
		headers = append([]string{fieldKey}, headers...)
	}

	return headers
}

func (l *IssueList) data() tui.TableData {
	var data tui.TableData

	headers := l.header()
	if !(l.Display.Plain && l.Display.NoHeaders) {
		data = append(data, headers)
	}
	if len(headers) == 0 {
		headers = ValidIssueColumns()
	}
	for _, iss := range l.Data {
		data = append(data, l.assignColumns(headers, iss))
	}

	return data
}

func (*IssueList) assignColumns(columns []string, issue *jira.Issue) []string {
	var bucket []string

	for _, column := range columns {
		switch column {
		case fieldType:
			bucket = append(bucket, issue.Fields.IssueType.Name)
		case fieldKey:
			bucket = append(bucket, issue.Key)
		case fieldSummary:
			bucket = append(bucket, prepareTitle(issue.Fields.Summary))
		case fieldStatus:
			bucket = append(bucket, issue.Fields.Status.Name)
		case fieldAssignee:
			bucket = append(bucket, issue.Fields.Assignee.Name)
		case fieldReporter:
			bucket = append(bucket, issue.Fields.Reporter.Name)
		case fieldPriority:
			bucket = append(bucket, issue.Fields.Priority.Name)
		case fieldResolution:
			bucket = append(bucket, issue.Fields.Resolution.Name)
		case fieldCreated:
			bucket = append(bucket, formatDateTime(issue.Fields.Created, jira.RFC3339))
		case fieldUpdated:
			bucket = append(bucket, formatDateTime(issue.Fields.Updated, jira.RFC3339))
		case fieldLabels:
			bucket = append(bucket, strings.Join(issue.Fields.Labels, ","))
		case fieldEpic:
			jsonStr := os.Getenv("JIRA_EPICS_LIST")
			//jsonStr := "[{\"key\":\"GPCC-318\", \"name\":\"GPCC-New-RG\"}, {\"key\":\"GPCC-293\", \"name\":\"GPCC-Alert\"}]"
			searchKey := issue.Fields.Epic

			var data []jira.KeyName
			var searchValue string

			err := json.Unmarshal([]byte(jsonStr), &data)
			if err != nil {
				searchValue="BadJson"
			}

			for _, kv := range data {
				if kv.Key == searchKey {
					searchValue = kv.Name
					break
				}
			}

			bucket = append(bucket, searchValue)
		//case fieldSprint:
		//		// Extract the JSON part from the string
		//		if  len(issue.Fields.Sprint)>0 {
		//      // The returned object is a Java object array
		//      // TODO iterate the array
		//		  str := issue.Fields.Sprint[0]
    //	    jsonStr := str[strings.Index(str, "[")+1 : strings.LastIndex(str, "]")]
    //
    //	    // Create an instance of the struct
		//      // The returned JSON object is not same with jira.Sprint
    //	    //sprint := jira.Sprint{}
    //
    //	    // Unmarshal the JSON string into the struct
		//      // TODO check the status and ignore closed ones 
    //	    err := json.Unmarshal([]byte(jsonStr), &sprint)
    //	    // Print the result
		//			if err == nil {
  	//	      bucket = append(bucket, sprint.Name )
		//			}else{
		//			  bucket = append(bucket, "bad")
		//			}
  	//    }
		}
	}
	return bucket
}
