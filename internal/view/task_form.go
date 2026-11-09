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
	f.problems = nil
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

// taskDialog draws the task sheet, or the question or editor that takes its
// place while the person answers it.
func (a *App) taskDialog(c *ui.Context) {
	f := &a.taskForm
	if !f.open {
		return
	}
	_, height := c.Size()
	opts := sheetOptions{width: 680, bodyMax: height - 232}
	var body, footer func()
	switch {
	case f.collision != nil:
		opts.width = 520
		opts.title = locales.TaskDestinationExists()
		body = func() { a.destinationQuestion(c) }
		footer = func() { a.destinationQuestionFoot(c) }
	case f.browser != nil:
		opts.title = locales.BrowseTitle(f.browser.storage.Name)
		opts.subtitle = locales.TaskFieldDestination()
		if f.browser.target == &f.source {
			opts.subtitle = locales.TaskFieldSource()
		}
		body = func() { a.folderBrowserBody(c) }
		footer = func() { a.folderBrowserFoot(c) }
	case f.askBackground:
		opts.width = 560
		opts.title = locales.BackgroundAskTitle()
		body = func() { a.backgroundQuestion(c) }
		footer = func() { a.backgroundQuestionFoot(c) }
	case f.exclude.open:
		opts.title = locales.TaskExcludeTitle()
		opts.subtitle = locales.TaskExcludeHint()
		body = func() { a.excludeEditorBody(c, f) }
		footer = func() { a.excludeEditorFoot(c, f) }
	default:
		opts.title = locales.TaskDialogNew()
		if f.id != "" {
			opts.title = locales.TaskDialogEdit()
		}
		opts.subtitle = locales.TaskDialogHint()
		body = func() { a.taskFormBody(c, f) }
		footer = func() { a.taskFormFoot(c, f) }
	}
	sheet(c, &f.open, opts, body, footer)
}

// taskFormBody is the form: one card a subject, in the order a person
// thinks about a task: what it is called, where the files come from and go
// to, how they are handled, and when it runs.
func (a *App) taskFormBody(c *ui.Context, f *taskForm) {
	p := paletteFor(c.Theme())
	ui.Column(c).Gap(gutter).Children(func() {
		formGroup(c, icons.Tasks, locales.TaskGroupBasics(), func() {
			ui.Field(c, locales.TaskFieldName(), func() {
				ui.TextInput(c, &f.name).AutoFocus()
			}).Error(problemText(f.problems.For(domain.FieldName)))

			hint := locales.ModeCopyHint()
			if modes[f.mode] == domain.ModeSync {
				hint = locales.ModeSyncHint()
			}
			ui.Field(c, locales.TaskFieldMode(), func() {
				// A row lets the control keep the width of its segments.
				ui.Row(c).Children(func() {
					ui.Segmented(c, &f.mode, locales.ModeCopy(), locales.ModeSync()).
						Label(locales.TaskFieldMode())
				})
			}).Description(hint).Error(problemText(f.problems.For(domain.FieldMode)))
		})

		formGroup(c, icons.Folder, locales.TaskFieldSource(), func() {
			a.locationControl(c, locales.TaskFieldSource(), &f.source, domain.FieldSource)
		})

		formGroup(c, icons.Upload, locales.TaskFieldDestination(), func() {
			a.locationControl(c, locales.TaskFieldDestination(), &f.dest, domain.FieldDestination)
			ui.Column(c).Gap(3).Children(func() {
				ui.Checkbox(c, &f.allowRoot, locales.TaskAllowRoot())
				ui.Text(c, locales.TaskAllowRootHint()).FontSize(sizeMicro).
					TextColor(p.muted).LineHeight(1.45)
			})
		})

		formGroup(c, icons.Transfer, locales.TaskFieldHow(), func() {
			ui.Field(c, locales.TaskFieldTrash(), func() {
				a.trashControl(c, f)
			}).Description(a.trashHint(f)).Error(problemText(f.problems.For(domain.FieldTrash)))

			ui.Field(c, locales.TaskFieldExclude(), func() {
				a.excludeInput(c, f)
			}).Description(a.excludeDescription(f)).Error(a.excludeError(f))
		})

		formGroup(c, icons.Calendar, locales.TaskFieldSchedule(), func() {
			a.scheduleField(c, f)
		})
	})
}

// taskFormFoot is the sheet's foot: what the save failed on, if anything,
// and the buttons that cancel or save it.
func (a *App) taskFormFoot(c *ui.Context, f *taskForm) {
	sheetError(c, a.taskFormFootError(f))
	sheetActions(c, func() {
		if accentButton(c, "task-cancel", nil, locales.ButtonCancel(), buttonSoft).Disabled(f.saving).Clicked() {
			f.open = false
		}
		if accentButton(c, "task-save", nil, locales.TaskSave(), buttonPrimary).Disabled(f.saving).Clicked() {
			a.saveTask()
		}
	})
}

// taskFormFootError says why the save did not go through, when a field does
// not already say it: the form's own problems show under their fields.
func (a *App) taskFormFootError(f *taskForm) string {
	for _, problem := range f.problems {
		switch problem.Field {
		case domain.FieldName, domain.FieldSource, domain.FieldDestination,
			domain.FieldSchedule, domain.FieldTrash, domain.FieldExclude, domain.FieldMode:
		default:
			return problemText(problem.Code)
		}
	}
	if len(f.problems) > 0 {
		return locales.TaskFixFields()
	}
	return ""
}

// locationControl is one side of a task: where it is, the path there, and
// the button that chooses the folder.
func (a *App) locationControl(c *ui.Context, label string, f *locationField, field domain.Field) {
	p := paletteFor(c.Theme())
	options := a.locationOptions()
	if indexOf(options, f.where) < 0 {
		f.where = options[0]
	}
	local := f.where == options[0]
	share, export := false, false
	var storage domain.Storage
	if i := indexOf(options, f.where); i > 0 {
		storage = a.storages[i-1]
		share = storage.Kind == domain.KindSMB
		export = storage.Kind == domain.KindNFS
	}
	placeholder := locales.TaskFieldFolder()
	desc := ""
	switch {
	case share:
		placeholder, desc = locales.TaskFieldSharePath(), locales.TaskFieldSharePathHint()
	case export:
		placeholder, desc = locales.TaskFieldExportPath(), locales.TaskFieldExportPathHint()
	case !local:
		placeholder, desc = locales.TaskFieldPrefix(), locales.TaskFieldPrefixHint()
	}
	// Two storages: the files go through this computer.
	if field == domain.FieldDestination && !a.location(a.taskForm.source).Local() && !a.location(a.taskForm.dest).Local() {
		desc += " " + locales.TaskRelayHint()
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Select(c, &f.where, options).Label(label).FillWidth()
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c, &f.path).Placeholder(placeholder).Label(placeholder).Grow(1).MinWidth(0)
			if local {
				if iconButton(c, icons.Folder, locales.TaskChooseFolder(), false).Clicked() {
					a.chooseFolder(f)
				}
			} else if iconButton(c, icons.Folder, locales.TaskBrowse(), false).Clicked() {
				a.browseFolder(f, storage)
			}
		})
		if desc != "" {
			ui.Text(c, desc).FontSize(sizeMicro).TextColor(p.muted).LineHeight(1.45)
		}
		sheetError(c, problemText(a.taskForm.problems.For(field)))
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

// trashControl chooses how long the task's runs keep what they delete or
// replace.
func (a *App) trashControl(c *ui.Context, f *taskForm) {
	labels := a.taskTrashLabels()
	chosen := labelAt(taskTrashValues, labels, f.trash)
	ui.Row(c).Children(func() {
		if ui.Select(c, &chosen, labels).Label(locales.TaskFieldTrash()).Width(240).Changed() {
			f.trash = valueAt(taskTrashValues, labels, chosen)
		}
	})
}

// trashHint says what the trash choice means for the files.
func (a *App) trashHint(f *taskForm) string {
	if (domain.Task{TrashDays: f.trash}).Trash(a.settings) == 0 {
		return locales.TaskTrashOffHint()
	}
	return locales.TaskTrashHint()
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

// destinationQuestion asks how to save a task whose destination name is
// already taken.
func (a *App) destinationQuestion(c *ui.Context) {
	p := paletteFor(c.Theme())
	f := &a.taskForm
	message := locales.TaskDestinationExistsHint()
	if !f.collision.Directory {
		message = locales.TaskDestinationFileHint()
	}
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			iconTile(c, icons.Folder, 38, p.warning, p.warningSoft)
			ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
				ui.Box(c).Radius(9).Background(p.cardAlt).Border(1, p.hairline).Padding(8, 11).
					Children(func() {
						ui.Text(c, f.collision.Path).FontSize(sizeSmall).TextColor(p.text).
							LineHeight(1.4).FontFeatures("tnum")
					})
				ui.Text(c, message).FontSize(sizeBody).TextColor(p.muted).LineHeight(1.5)
			})
		})
	})
}

// destinationQuestionFoot chooses between the two ways to go on.
func (a *App) destinationQuestionFoot(c *ui.Context) {
	f := &a.taskForm
	sheetActions(c, func() {
		if accentButton(c, "collision-cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
			f.collision = nil
		}
		if f.collision != nil && f.collision.Directory {
			if accentButton(c, "collision-reuse", nil, locales.TaskDestinationReuse(), buttonSoft).Clicked() {
				in := f.pending
				in.DestinationChoice = "reuse"
				a.submitTask(in)
			}
		}
		if accentButton(c, "collision-unique", nil, locales.TaskDestinationUnique(), buttonPrimary).Clicked() {
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
	p := paletteFor(c.Theme())
	ui.Row(c).Gap(13).AlignItems(ui.Start).Children(func() {
		iconTile(c, icons.Bell, 38, p.accent, p.accentSoft)
		ui.Column(c).Grow(1).MinWidth(0).Gap(10).Children(func() {
			ui.Text(c, locales.BackgroundAskMessage()).FontSize(sizeBody).TextColor(p.text).LineHeight(1.5)
			ui.Text(c, locales.BackgroundAskNote()).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.5)
		})
	})
}

// backgroundQuestionFoot answers it.
func (a *App) backgroundQuestionFoot(c *ui.Context) {
	sheetActions(c, func() {
		if accentButton(c, "background-decline", nil, locales.BackgroundDecline(), buttonSoft).Clicked() {
			a.answerBackground(false)
		}
		if accentButton(c, "background-allow", nil, locales.BackgroundAllow(), buttonPrimary).Clicked() {
			a.answerBackground(true)
		}
	})
}
