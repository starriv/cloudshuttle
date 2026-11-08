package view

import (
	"context"
	"errors"
	"log/slog"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// taskForm is the dialog that adds or edits a task.
type taskForm struct {
	generation uint64
	open       bool
	allowRoot  bool
	saving     bool
	collision  *service.DestinationExists
	pending    domain.TaskInput
	id         string // the task edited, "" for a new one
	name       string
	source     locationField
	dest       locationField
	mode       int // 0 copy, 1 sync, as the segmented control shows them
	schedule   scheduleField
	trash      int // as domain.Task.TrashDays
	exclude    excludeField
	problems   domain.Problems
	browser    *folderBrowser // open over the form while choosing a folder of a storage
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
	f := taskForm{generation: a.taskForm.generation + 1, open: true, schedule: newScheduleField(domain.Schedule{}, a.now()), exclude: newExcludeField(domain.NewExclude())}
	if task == nil {
		f.source = a.locationField(domain.Location{})
		f.dest = a.locationField(domain.Location{})
		if len(a.storages) > 0 {
			// A new task usually backs this computer up to a storage.
			f.dest = a.locationField(domain.Location{StorageID: a.storages[0].ID})
		}
	} else {
		f.id, f.name, f.schedule = task.ID, task.Name, newScheduleField(task.Schedule, a.now())
		f.source, f.dest = a.locationField(task.Source), a.locationField(service.DestinationParent(*task))
		f.trash, f.exclude = task.TrashDays, newExcludeField(task.Exclude)
		f.allowRoot = task.AllowRoot
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
		AllowRoot:   f.allowRoot,
		Source:      a.location(f.source),
		Destination: a.location(f.dest),
		Mode:        modes[f.mode],
		Schedule:    f.schedule.schedule(),
		TrashDays:   f.trash,
		Exclude:     f.exclude.exclude(),
	}
	a.submitTask(in)
}

// submitTask checks remote names and saves credentials off the UI thread.
func (a *App) submitTask(in domain.TaskInput) {
	f := &a.taskForm
	if f.saving {
		return
	}
	f.saving = true
	f.collision = nil
	f.pending = in
	id := f.id
	generation := f.generation
	go func() {
		ctx := context.Background()
		var err error
		if id == "" {
			_, err = a.svc.CreateTask(ctx, in)
		} else {
			_, err = a.svc.UpdateTask(ctx, id, in)
		}
		a.host.Update(func() {
			if f.generation != generation || !f.open {
				return
			}
			f.saving = false
			var collision *service.DestinationExists
			if errors.As(err, &collision) {
				f.collision = collision
				return
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
		})
	}()
}

func (a *App) destinationQuestion(c *ui.Context) {
	f := &a.taskForm
	ui.Text(c, locales.TaskDestinationExists()).FontSize(18).FontWeight(500)
	ui.Text(c, f.collision.Path)
	message := locales.TaskDestinationExistsHint()
	if !f.collision.Directory {
		message = locales.TaskDestinationFileHint()
	}
	ui.Text(c, message)
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.ButtonCancel()).Clicked() {
			f.collision = nil
		}
		if f.collision != nil && f.collision.Directory {
			if ui.Button(c, locales.TaskDestinationReuse()).Clicked() {
				in := f.pending
				in.DestinationChoice = "reuse"
				a.submitTask(in)
			}
		}
		if ui.PrimaryButton(c, locales.TaskDestinationUnique()).Clicked() {
			in := f.pending
			in.DestinationChoice = "unique"
			a.submitTask(in)
		}
	})
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
			if f.collision != nil {
				a.destinationQuestion(c)
				return
			}
			if f.browser != nil {
				a.folderBrowserView(c)
				return
			}
			if f.askBackground {
				a.backgroundQuestion(c)
				return
			}
			if f.exclude.open {
				a.excludeEditor(c, f)
				return
			}
			title := locales.TaskDialogNew()
			if f.id != "" {
				title = locales.TaskDialogEdit()
			}
			ui.Text(c, title).FontSize(18).FontWeight(500)

			_, height := c.Size()
			ui.Scroll(c).MaxHeight(height - 210).Children(func() {
				ui.Form(c, func() {
					ui.Field(c, locales.TaskFieldName(), func() {
						ui.TextInput(c, &f.name).AutoFocus()
					}).Error(problemText(f.problems.For(domain.FieldName)))
					a.locationInput(c, locales.TaskFieldSource(), &f.source, domain.FieldSource)
					a.locationInput(c, locales.TaskFieldDestination(), &f.dest, domain.FieldDestination)
					ui.Field(c, locales.TaskAllowRoot(), func() {
						ui.Checkbox(c, &f.allowRoot, "").Label(locales.TaskAllowRoot())
					}).Description(locales.TaskAllowRootHint())
					hint := locales.ModeCopyHint()
					if modes[f.mode] == domain.ModeSync {
						hint = locales.ModeSyncHint()
					}
					ui.Field(c, locales.TaskFieldMode(), func() {
						ui.Segmented(c, &f.mode, locales.ModeCopy(), locales.ModeSync())
					}).Description(hint)
					a.trashInput(c, f)
					a.excludeInput(c, f)
					a.scheduleInput(c, f)
				})
			})

			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, locales.ButtonCancel()).Disabled(f.saving).Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, locales.TaskSave()).Disabled(f.saving).Clicked() {
					a.saveTask()
				}
			})
		})
	})
}

// taskTrashValues are what a task's trash select offers, as TrashDays.
var taskTrashValues = []int{domain.TrashFollow, domain.TrashOff, 7, 30, 90}

// taskTrashLabels name taskTrashValues: the first says what the settings
// choose, for the task that follows them.
func (a *App) taskTrashLabels() []string {
	labels := make([]string, len(taskTrashValues))
	for i, days := range taskTrashValues {
		switch {
		case days == domain.TrashFollow && a.settings.TrashDays > 0:
			labels[i] = locales.TaskTrashFollow(a.settings.TrashDays)
		case days == domain.TrashFollow:
			labels[i] = locales.TaskTrashFollowOff()
		case days == domain.TrashOff:
			labels[i] = locales.SettingsTrashOff()
		default:
			labels[i] = locales.SettingsKeepDays(days)
		}
	}
	return labels
}

// trashInput chooses how long the task's runs keep what they delete or
// replace.
func (a *App) trashInput(c *ui.Context, f *taskForm) {
	labels := a.taskTrashLabels()
	chosen := labelAt(taskTrashValues, labels, f.trash)
	hint := locales.TaskTrashHint()
	if (domain.Task{TrashDays: f.trash}).Trash(a.settings) == 0 {
		hint = locales.TaskTrashOffHint()
	}
	ui.Field(c, locales.TaskFieldTrash(), func() {
		if ui.Select(c, &chosen, labels).Label(locales.TaskFieldTrash()).Width(220).Changed() {
			f.trash = valueAt(taskTrashValues, labels, chosen)
		}
	}).Description(hint).Error(problemText(f.problems.For(domain.FieldTrash)))
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
