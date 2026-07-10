#include <cstdio>
#include <cstring>
#include <ctime>

#include "inkview.h"

static const int kFontSize = 28;
static const int kSleepSeconds = 120;
static const int kSettleMilliseconds = 2000;

enum ProbeState {
    PROBE_READY,
    PROBE_PENDING,
    PROBE_FINISHED,
};

static ifont *font = NULL;
static ProbeState probe_state = PROBE_READY;
static time_t sleep_started_at = 0;
static time_t sleep_finished_at = 0;
static time_t ignore_start_until = 0;
static int sleep_result = 0;
static int battery_before = 0;
static int battery_after = 0;

static void enter_sleep();

static void format_time(time_t value, char *text, size_t text_size) {
    if (value <= 0) {
        snprintf(text, text_size, "not available");
        return;
    }
    struct tm *local = localtime(&value);
    if (local == NULL ||
        strftime(text, text_size, "%Y-%m-%d %H:%M:%S", local) == 0) {
        snprintf(text, text_size, "not available");
    }
}

static void draw_line(int y, const char *text) {
    DrawTextRect(kFontSize * 2, y, ScreenWidth() - kFontSize * 4,
                 kFontSize * 2, text, ALIGN_LEFT);
}

static void draw_probe() {
    ClearScreen();
    SetFont(font, BLACK);

    int y = kFontSize * 3;
    char line[160];
    DrawTextRect(0, y, ScreenWidth(), kFontSize * 3,
                 "PocketFrame sleep probe", ALIGN_CENTER);
    y += kFontSize * 5;

    const char *model = GetDeviceModel();
    const char *software = GetSoftwareVersion();
    snprintf(line, sizeof(line), "Device: %s",
             model == NULL ? "unknown" : model);
    draw_line(y, line);
    y += kFontSize * 2;
    snprintf(line, sizeof(line), "Software: %s",
             software == NULL ? "unknown" : software);
    draw_line(y, line);
    y += kFontSize * 3;

    if (probe_state == PROBE_READY) {
        snprintf(line, sizeof(line), "Test duration: %d seconds",
                 kSleepSeconds);
        draw_line(y, line);
        y += kFontSize * 2;
        draw_line(y, "Press OK to start one sleep cycle.");
        y += kFontSize * 2;
        draw_line(y, "Back or Home exits. No automatic repeat.");
    } else if (probe_state == PROBE_PENDING) {
        draw_line(y, "Preparing to sleep...");
        y += kFontSize * 2;
        draw_line(y, "The screen will remain unchanged.");
        y += kFontSize * 2;
        draw_line(y, "Wait for automatic RTC wakeup.");
    } else {
        char started[32];
        char finished[32];
        format_time(sleep_started_at, started, sizeof(started));
        format_time(sleep_finished_at, finished, sizeof(finished));
        long elapsed = static_cast<long>(sleep_finished_at - sleep_started_at);

        snprintf(line, sizeof(line), "Started: %s", started);
        draw_line(y, line);
        y += kFontSize * 2;
        snprintf(line, sizeof(line), "Finished: %s", finished);
        draw_line(y, line);
        y += kFontSize * 2;
        snprintf(line, sizeof(line), "Elapsed: %ld sec, result: %d",
                 elapsed, sleep_result);
        draw_line(y, line);
        y += kFontSize * 2;
        snprintf(line, sizeof(line), "Battery: %d%% -> %d%%",
                 battery_before, battery_after);
        draw_line(y, line);
        y += kFontSize * 3;

        if (elapsed >= kSleepSeconds - 5) {
            draw_line(y, "Result: likely RTC wakeup.");
        } else {
            draw_line(y, "Result: interrupted or suspend unsupported.");
        }
        y += kFontSize * 2;
        draw_line(y, "Wait 3 sec, then press OK to repeat.");
    }

    FullUpdate();
}

static void start_probe() {
    if (probe_state == PROBE_PENDING || time(NULL) < ignore_start_until) {
        return;
    }
    ClearTimer(enter_sleep);
    probe_state = PROBE_PENDING;
    draw_probe();
    SetWeakTimer("PocketFrameSleepProbe", enter_sleep,
                 kSettleMilliseconds);
}

static void enter_sleep() {
    if (probe_state != PROBE_PENDING) {
        return;
    }

    battery_before = GetBatteryPower();
    sleep_started_at = time(NULL);
    iv_sleepmode(1);
    sleep_result = GoSleep(kSleepSeconds * 1000, 0);
    iv_sleepmode(0);
    sleep_finished_at = time(NULL);
    battery_after = GetBatteryPower();
    ignore_start_until = sleep_finished_at + 3;
    probe_state = PROBE_FINISHED;
    draw_probe();
}

static int main_handler(int event_type, int param_one, int param_two) {
    if (event_type == EVT_INIT) {
        font = OpenFont("LiberationSans", kFontSize, 0);
        draw_probe();
    } else if (event_type == EVT_SHOW || event_type == EVT_REPAINT ||
               event_type == EVT_FOREGROUND || event_type == EVT_ACTIVATE) {
        draw_probe();
    } else if (event_type == EVT_KEYPRESS) {
        if (param_one == KEY_OK) {
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
