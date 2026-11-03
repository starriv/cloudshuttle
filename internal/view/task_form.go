package view

import (
	"context"
	"log/slog"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// taskForm is the dialog that adds or edits a task.
type taskForm struct {
	open     bool
	id       string // the task edited, "" for a new one
	name     string
	source   locationField
	dest     locationField
	mode     int // 0 copy, 1 sync, as the segmented control shows them
	schedule scheduleField
	problems domain.Problems
	browser  *folderBrowser // open over the form while choosing a folder of a storage
	// askBackground asks, once the task is saved, whether the system may
	// run the schedules while the app is closed: the first schedule needs
	// the answer.
	askBackground bool
}

// locationField is one side of a task: where (this computer or a storage,
// as the select's label) and the path there.
type locationField struct {
	where string
	path  string
}

var modes = []domain.Mode{domain.ModeCopy, domain.ModeSync}

// locationOptions are the labels of the location select: this computer,
// then the storages.
func (a *App) locationOptions() []string {
	labels := []string{locales.LocationThisComputer()}
	for _, s := range a.storages {
		labels = append(labels, s.Name)
	}
	return uniqueLabels(labels)
}

func (a *App) locationField(l domain.Location) locationField {
	options := a.locationOptions()
	f := locationField{where: options[0], path: l.Path}
	for i, s := range a.storages {
		if s.ID == l.StorageID {
			f.where = options[i+1]
		}
	}
	return f
}

func (a *App) location(f locationField) domain.Location {
	if i := indexOf(a.locationOptions(), f.where); i > 0 {
		return domain.Location{StorageID: a.storages[i-1].ID, Path: f.path}
	}
	return domain.Location{Path: f.path}
}

func (a *App) openTaskForm(task *domain.Task) {
	f := taskForm{open: true, schedule: newScheduleField(domain.Schedule{}, a.now())}
	if task == nil {
		f.source = a.locationField(domain.Location{})
		f.dest = a.locationField(domain.Location{})
		if len(a.storages) > 0 {
			// A new task usually backs this computer up to a storage.
			f.dest = a.locationField(domain.Location{StorageID: a.storages[0].ID})
		}
	} else {
		f.id, f.name, f.schedule = task.ID, task.Name, newScheduleField(task.Schedule, a.now())
		f.source, f.dest = a.locationField(task.Source), a.locationField(task.Destination)
		if task.Mode == domain.ModeSync {
			f.mode = 1
		}
	}
	a.taskForm = f
}

func (a *App) saveTask() {
	f := &a.taskForm
	in := domain.TaskInput{
		Name:        f.name,
		Source:      a.location(f.source),
		Destination: a.location(f.dest),
		Mode:        modes[f.mode],
		Schedule:    f.schedule.schedule(),
	}
	ctx := context.Background()
	var err error
	if f.id == "" {
		_, err = a.svc.CreateTask(ctx, in)
	} else {
		_, err = a.svc.UpdateTask(ctx, f.id, in)
	}
	if problems := domain.AsProblems(err); problems != nil {
		f.problems = problems
		return
	}
	if err != nil {
		a.toastError(err)
		return
	}
	a.toast(locales.TaskSaved())
	if in.Schedule.On() && a.svc.SystemScheduler() && a.svc.Settings().Background == domain.ConsentUnasked {
		f.askBackground = true
		return
	}
	f.open = false
}

// answerBackground keeps whether the system may run the schedules while
// the app is closed, as the person answered, and closes the form.
func (a *App) answerBackground(allow bool) {
	if err := a.svc.AllowBackground(context.Background(), allow); err != nil {
		a.toastError(err)
		return
	}
	a.settings = a.svc.Settings()
	a.settingsForm.background = allow
	a.taskForm.open = false
}

// backgroundQuestion asks whether the system may start the app for its
// schedules: it adds a background item, which the person should know of
// before it appears.
func (a *App) backgroundQuestion(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, locales.BackgroundAskTitle()).FontSize(18).FontWeight(500)
	ui.Text(c, locales.BackgroundAskMessage())
	ui.Text(c, locales.BackgroundAskNote()).FontSize(12).TextColor(t.TextMuted)
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.BackgroundDecline()).Clicked() {
			a.answerBackground(false)
		}
		if ui.PrimaryButton(c, locales.BackgroundAllow()).Clicked() {
			a.answerBackground(true)
		}
	})
}

func (a *App) taskDialog(c *ui.Context) {
	f := &a.taskForm
	ui.Modal(c, &f.open, func() {
		t := c.Theme()
		ui.Column(c).Width(540).Gap(t.Space(4)).Children(func() {
			if f.browser != nil {
				a.folderBrowserView(c)
				return
			}
			if f.askBackground {
				a.backgroundQuestion(c)
				return
			}
			title := locales.TaskDialogNew()
			if f.id != "" {
				title = locales.TaskDialogEdit()
			}
			ui.Text(c, title).FontSize(18).FontWeight(500)

			ui.Form(c, func() {
				ui.Field(c, locales.TaskFieldName(), func() {
					ui.TextInput(c, &f.name).AutoFocus()
				}).Error(problemText(f.problems.For(domain.FieldName)))
				a.locationInput(c, locales.TaskFieldSource(), &f.source, domain.FieldSource)
				a.locationInput(c, locales.TaskFieldDestination(), &f.dest, domain.FieldDestination)
				hint := locales.ModeCopyHint()
				if modes[f.mode] == domain.ModeSync {
					hint = locales.ModeSyncHint()
				}
				ui.Field(c, locales.TaskFieldMode(), func() {
					ui.Segmented(c, &f.mode, locales.ModeCopy(), locales.ModeSync())
				}).Description(hint)
				a.scheduleInput(c, f)
			})

			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.ButtonCancel()).Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, locales.TaskSave()).Clicked() {
					a.saveTask()
				}
			})
		})
	})
}

func (a *App) locationInput(c *ui.Context, label string, f *locationField, field domain.Field) {
	options := a.locationOptions()
	if indexOf(options, f.where) < 0 {
		f.where = options[0]
	}
	local := f.where == options[0]
	share := false
	var storage domain.Storage
	if i := indexOf(options, f.where); i > 0 {
		storage = a.storages[i-1]
		share = storage.Kind == domain.KindSMB
	}
	desc := ""
	switch {
	case share:
		desc = locales.TaskFieldSharePathHint()
	case !local:
		desc = locales.TaskFieldPrefixHint()
	}
	// Two storages: the files go through this computer.
	tf := &a.taskForm
	if field == domain.FieldDestination && !a.location(tf.source).Local() && !a.location(tf.dest).Local() {
		desc += " " + locales.TaskRelayHint()
	}
	ui.Field(c, label, func() {
		ui.Column(c).Gap(6).FillWidth().Children(func() {
			ui.Select(c, &f.where, options).Label(label)
			ui.Row(c).Gap(6).Children(func() {
				placeholder := locales.TaskFieldFolder()
				switch {
				case share:
					placeholder = locales.TaskFieldSharePath()
				case !local:
					placeholder = locales.TaskFieldPrefix()
				}
				ui.TextInput(c, &f.path).Placeholder(placeholder).Label(placeholder).Grow(1).MinWidth(0)
				switch {
				case local:
					if iconButton(c, icons.Folder, locales.TaskChooseFolder(), false).Clicked() {
						a.chooseFolder(f)
					}
				default:
					if iconButton(c, icons.Folder, locales.TaskBrowse(), false).Clicked() {
						a.browseFolder(f, storage)
					}
				}
			})
		})
	}).Description(desc).Error(problemText(a.taskForm.problems.For(field)))
}

// chooseFolder asks for a folder off the main thread, and puts the answer
// into f on it.
func (a *App) chooseFolder(f *locationField) {
	go func() {
		path, err := a.host.ChooseFolder()
		if err != nil {
			slog.Error("choose folder", "err", err)
			return
		}
		if path != "" {
			a.host.Update(func() { f.path = path })
		}
	}()
}
