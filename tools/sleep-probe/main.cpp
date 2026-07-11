#include <cstdio>
#include <cstring>
#include <ctime>

#include "inkview.h"

static const int kFontSize = 28;
static const int kSettleMilliseconds = 5000;
static const int kDurationCount = 5;
static const int kDurationsMinutes[kDurationCount] = {5, 15, 30, 45, 60};

enum ProbeState {
    PROBE_READY,
    PROBE_RUNNING,
    PROBE_FINISHED,
};

struct ProbeResult {
    bool completed;
    int requested_seconds;
    int elapsed_seconds;
    int result_code;
};

static ifont *font = NULL;
static ProbeState probe_state = PROBE_READY;
static ProbeResult results[kDurationCount];
static int current_test = 0;
static int battery_before = 0;
static int battery_after = 0;
static time_t suite_started_at = 0;
static time_t suite_finished_at = 0;
static time_t ignore_start_until = 0;

static void enter_sleep();

static void format_time(time_t value, char *text, size_t text_size) {
    if (value <= 0) {
        snprintf(text, text_size, "--");
        return;
    }
    struct tm *local = localtime(&value);
    if (local == NULL ||
        strftime(text, text_size, "%Y-%m-%d %H:%M:%S", local) == 0) {
        snprintf(text, text_size, "--");
    }
}

static void draw_line(int y, const char *text) {
    DrawTextRect(kFontSize * 2, y, ScreenWidth() - kFontSize * 4,
                 kFontSize * 2, text, ALIGN_LEFT);
}

static bool result_passed(const ProbeResult *result) {
    return result->completed &&
           result->elapsed_seconds >= result->requested_seconds - 5;
}

static void draw_probe() {
    ClearScreen();
    SetFont(font, BLACK);

    int y = kFontSize * 3;
    char line[160];
    DrawTextRect(0, y, ScreenWidth(), kFontSize * 3,
                 "PocketFrame RTC duration probe", ALIGN_CENTER);
    y += kFontSize * 4;

    snprintf(line, sizeof(line), "Device: %s  Software: %s",
             GetDeviceModel() == NULL ? "unknown" : GetDeviceModel(),
             GetSoftwareVersion() == NULL ? "unknown" : GetSoftwareVersion());
    draw_line(y, line);
    y += kFontSize * 3;

    if (probe_state == PROBE_READY) {
        draw_line(y, "Tests: 5, 15, 30, 45, and 60 minutes.");
        y += kFontSize * 2;
        draw_line(y, "Total time if all pass: about 2 h 36 min.");
        y += kFontSize * 2;
        draw_line(y, "Press Right/Page Forward once to start.");
        y += kFontSize * 2;
        draw_line(y, "Do not press keys during the test. Back/Home exits.");
        FullUpdate();
        return;
    }

    char started[32];
    char finished[32];
    format_time(suite_started_at, started, sizeof(started));
    format_time(suite_finished_at, finished, sizeof(finished));
    snprintf(line, sizeof(line), "Started: %s", started);
    draw_line(y, line);
    y += kFontSize * 2;
    snprintf(line, sizeof(line), "Finished: %s", finished);
    draw_line(y, line);
    y += kFontSize * 2;
    snprintf(line, sizeof(line), "Battery: %d%% -> %d%%",
             battery_before, battery_after);
    draw_line(y, line);
    y += kFontSize * 3;

    for (int index = 0; index < kDurationCount; ++index) {
        const ProbeResult *result = &results[index];
        if (result->completed) {
            snprintf(line, sizeof(line), "%2d min: %4d sec, rc=%d, %s",
                     kDurationsMinutes[index], result->elapsed_seconds,
                     result->result_code,
                     result_passed(result) ? "RTC OK" : "EARLY RETURN");
        } else if (probe_state == PROBE_RUNNING && index == current_test) {
            snprintf(line, sizeof(line), "%2d min: next test in 5 seconds",
                     kDurationsMinutes[index]);
        } else {
            snprintf(line, sizeof(line), "%2d min: not tested",
                     kDurationsMinutes[index]);
        }
        draw_line(y, line);
        y += kFontSize * 2;
    }

    y += kFontSize;
    if (probe_state == PROBE_RUNNING) {
        draw_line(y, "Test running. The screen stays unchanged while asleep.");
    } else {
        draw_line(y, "Finished. Photograph this screen; Right restarts.");
    }
    FullUpdate();
}

static void finish_probe() {
    ClearTimer(enter_sleep);
    suite_finished_at = time(NULL);
    ignore_start_until = suite_finished_at + 3;
    battery_after = GetBatteryPower();
    probe_state = PROBE_FINISHED;
    draw_probe();
}

static void start_probe() {
    if (probe_state == PROBE_RUNNING || time(NULL) < ignore_start_until) {
        return;
    }
    ClearTimer(enter_sleep);
    memset(results, 0, sizeof(results));
    current_test = 0;
    suite_started_at = time(NULL);
    suite_finished_at = 0;
    battery_before = GetBatteryPower();
    battery_after = battery_before;
    probe_state = PROBE_RUNNING;
    draw_probe();
    SetWeakTimer("PocketFrameSleepProbe", enter_sleep,
                 kSettleMilliseconds);
}

static void enter_sleep() {
    if (probe_state != PROBE_RUNNING || current_test >= kDurationCount) {
        return;
    }

    ProbeResult *result = &results[current_test];
    result->requested_seconds = kDurationsMinutes[current_test] * 60;
    time_t started_at = time(NULL);
    iv_sleepmode(1);
    result->result_code = GoSleep(result->requested_seconds * 1000, 0);
    iv_sleepmode(0);
    time_t finished_at = time(NULL);
    result->elapsed_seconds = static_cast<int>(finished_at - started_at);
    result->completed = true;
    battery_after = GetBatteryPower();

    if (!result_passed(result)) {
        finish_probe();
        return;
    }

    ++current_test;
    if (current_test >= kDurationCount) {
        finish_probe();
        return;
    }

    draw_probe();
    SetWeakTimer("PocketFrameSleepProbe", enter_sleep,
                 kSettleMilliseconds);
}

static int main_handler(int event_type, int param_one, int param_two) {
    if (event_type == EVT_INIT) {
        font = OpenFont("LiberationSans", kFontSize, 0);
        draw_probe();
    } else if (event_type == EVT_SHOW || event_type == EVT_REPAINT ||
               event_type == EVT_FOREGROUND || event_type == EVT_ACTIVATE) {
        draw_probe();
    } else if (event_type == EVT_KEYPRESS) {
        if (param_one == KEY_RIGHT || param_one == KEY_NEXT ||
            param_one == KEY_NEXT2 || param_one == KEY_OK) {
            start_probe();
            return 1;
        }
        if (param_one == KEY_BACK || param_one == KEY_HOME) {
            CloseApp();
            return 1;
        }
    } else if (event_type == EVT_EXIT) {
        ClearTimer(enter_sleep);
        iv_sleepmode(0);
        if (font != NULL) {
            CloseFont(font);
            font = NULL;
        }
    }
    return 0;
}

int main(int argc, char *argv[]) {
    InkViewMain(main_handler);
    return 0;
}
