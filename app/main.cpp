#include <dirent.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cctype>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <ctime>

#include "inkview.h"

static ifont *font;
static int y_log;
static bool debug = false;
static const int kFontSize = 12;
static const int PICTURE_DISPLAY_TIME = 300;  // Legacy local slideshow: 5 min.
static const int kDefaultRefreshSeconds = 60 * 60;
static const int kMinRefreshSeconds = 5 * 60;
static const int kDefaultRetrySeconds = 30 * 60;
static const int kDefaultRetryMaxSeconds = 4 * 60 * 60;
static const int kMaxPollSeconds = 24 * 60 * 60;
static const int kDefaultTimeoutSeconds = 15;
static const int kMinTimeoutSeconds = 5;
static const int kMaxTimeoutSeconds = 60;
static const int kDefaultWakeMinBatteryPercent = 20;
static const int kSleepSettleMilliseconds = 2000;
static const int kWakeSettleMilliseconds = 1000;
static const int kImmediateSleepFailureSeconds = 3;
static const int kMaxImmediateSleepFailures = 3;
static const char *REMOTE_CONFIG_PATH =
    "/mnt/ext1/system/config/pocketframe.cfg";
static const char *REMOTE_CACHE_PATH =
    "/mnt/ext1/system/config/pocketframe-current.jpg";
static const char *REMOTE_TEMP_PATH =
    "/mnt/ext1/system/config/pocketframe-current.jpg.new";
static const char *REMOTE_STATE_PATH =
    "/mnt/ext1/system/config/pocketframe-state.cfg";
static const char *REMOTE_STATE_TEMP_PATH =
    "/mnt/ext1/system/config/pocketframe-state.cfg.new";
static const char *PICTURE_DIRS[] = {
    "/mnt/ext1/My pictures/PocketFrame/",
    "/mnt/ext1/My Pictures/PocketFrame/",
    "/mnt/ext2/My pictures/PocketFrame/",
    "/mnt/ext2/My Pictures/PocketFrame/",
};
static const int kMaxPath = 512;
static const int kConfigLineSize = 768;
static const int kMaxRevisionSize = 256;

enum RefreshMode {
    REFRESH_BATTERY_SAVER,
    REFRESH_ALWAYS_ON,
    REFRESH_SCHEDULED_SLEEP,
};

struct RemoteConfig {
    char url[kConfigLineSize];
    char manifest_url[kConfigLineSize];
    int refresh_seconds;
    int retry_seconds;
    int retry_max_seconds;
    int timeout_seconds;
    int wake_min_battery_percent;
    RefreshMode refresh_mode;
};

struct RemoteManifest {
    char revision[kMaxRevisionSize];
    char publication_id[kMaxRevisionSize];
    long publication_slot;
    bool has_update_ready;
    bool update_ready;
    int next_poll_seconds;
    int retry_after_seconds;
};

static const char *picture_dir = NULL;
static char current_picture_name[kMaxPath] = "";
static RemoteConfig remote_config;
static bool remote_mode = false;
static bool have_remote_hash = false;
static unsigned long long remote_hash = 0;
static size_t remote_size = 0;
static bool have_remote_revision = false;
static char remote_revision[kMaxRevisionSize] = "";
static bool have_remote_publication = false;
static char remote_publication_id[kMaxRevisionSize] = "";
static long remote_publication_slot = 0;
static bool remote_refresh_scheduled = false;
static int remote_next_poll_seconds = kDefaultRefreshSeconds;
static int remote_retry_seconds = kDefaultRetrySeconds;
static int remote_retry_max_seconds = kDefaultRetryMaxSeconds;
static time_t last_image_update_time = 0;
static time_t last_network_attempt_time = 0;
static time_t last_network_success_time = 0;
static int last_network_duration_seconds = 0;
static int successful_network_checks = 0;
static int failed_network_checks = 0;
static int consecutive_network_failures = 0;
static bool last_network_succeeded = false;
static bool diagnostics_visible = false;
static bool scheduled_sleep_disabled = false;
static bool scheduled_sleep_low_battery = false;
static int pending_sleep_seconds = 0;
static int immediate_sleep_failures = 0;
static int last_sleep_result = 0;
static int last_sleep_elapsed_seconds = 0;
static time_t last_sleep_started_time = 0;
static time_t last_sleep_finished_time = 0;
static time_t next_wake_time = 0;
static bool last_wake_likely_rtc = false;
static bool suppress_next_repaint = false;

static void show_next_picture();
static void refresh_remote_picture();
static void enter_scheduled_sleep();
static void release_network();
static void draw_diagnostics();
static void repaint_current_picture();

static void log_message(const char *msg) {
    if (!debug || strlen(msg) == 0) {
        return;
    }
    DrawTextRect(0, y_log, ScreenWidth(), kFontSize, msg, ALIGN_LEFT);
    PartialUpdate(0, y_log, ScreenWidth(), y_log + kFontSize + 2);
    y_log += kFontSize + 2;
}

static void show_status(const char *line_one, const char *line_two) {
    ClearScreen();
    y_log = 20;
    DrawTextRect(0, y_log, ScreenWidth(), kFontSize * 2, line_one, ALIGN_CENTER);
    y_log += kFontSize * 3;
    if (line_two != NULL && strlen(line_two) > 0) {
        DrawTextRect(0, y_log, ScreenWidth(), kFontSize * 4, line_two,
                     ALIGN_CENTER);
    }
    FullUpdate();
}

static int is_regular_file(const char *path) {
    struct stat path_stat;
    if (stat(path, &path_stat) != 0) {
        return 0;
    }
    return S_ISREG(path_stat.st_mode);
}

static bool is_jpeg_file(const char *name) {
    const char *dot = strrchr(name, '.');
    if (dot == NULL) {
        return false;
    }

    char ext[6] = {0};
    for (int i = 0; dot[i] != '\0' && i < 5; ++i) {
        ext[i] = static_cast<char>(tolower(dot[i]));
    }

    return strcmp(ext, ".jpg") == 0 || strcmp(ext, ".jpeg") == 0;
}

static const char *find_picture_dir() {
    for (size_t i = 0; i < sizeof(PICTURE_DIRS) / sizeof(PICTURE_DIRS[0]); ++i) {
        DIR *dir = opendir(PICTURE_DIRS[i]);
        if (dir != NULL) {
            closedir(dir);
            return PICTURE_DIRS[i];
        }
    }
    return NULL;
}

static bool build_picture_path(const char *name, char *path, size_t path_size) {
    int written = snprintf(path, path_size, "%s%s", picture_dir, name);
    return written > 0 && static_cast<size_t>(written) < path_size;
}

static void draw_picture(ibitmap *picture) {
    int screen_width = ScreenWidth();
    int screen_height = ScreenHeight();
    int draw_width = screen_width;
    int draw_height = screen_height;

    if (picture->width <= 0 || picture->height <= 0) {
        show_status("PocketFrame: invalid JPEG size", "");
        return;
    }

    if ((long long)picture->width * screen_height >
        (long long)picture->height * screen_width) {
        draw_height = (int)((long long)picture->height * screen_width /
                            picture->width);
    } else {
        draw_width = (int)((long long)picture->width * screen_height /
                           picture->height);
    }

    if (draw_width < 1) {
        draw_width = 1;
    }
    if (draw_height < 1) {
        draw_height = 1;
    }

    int x = (screen_width - draw_width) / 2;
    int y = (screen_height - draw_height) / 2;

    ClearScreen();
    Stretch(picture->data, picture->depth, picture->width, picture->height,
            picture->scanline, x, y, draw_width, draw_height, 0);
}

static bool load_and_draw_path(const char *path, bool report_error) {
    ibitmap *picture =
        LoadJPEG(path, ScreenWidth(), ScreenHeight(), 100, 100, 1);
    if (picture == NULL || picture->data == NULL) {
        if (report_error) {
            show_status("PocketFrame: JPEG load failed", path);
        }
        free(picture);
        return false;
    }

    draw_picture(picture);
    log_message(path);
    FullUpdate();
    free(picture);
    return true;
}

static bool load_and_draw_picture(const char *name) {
    char picfile[kMaxPath];
    if (!build_picture_path(name, picfile, sizeof(picfile))) {
        show_status("PocketFrame: path too long", name);
        return false;
    }
    return load_and_draw_path(picfile, true);
}

static bool find_next_picture(char *name, size_t name_size) {
    DIR *dir;
    struct dirent *ent;
    char first_picture[kMaxPath] = "";
    char next_picture[kMaxPath] = "";
    bool use_next_file = strlen(current_picture_name) == 0;

    if ((dir = opendir(picture_dir)) == NULL) {
        show_status("PocketFrame: could not open folder", picture_dir);
        return false;
    }

    while ((ent = readdir(dir)) != NULL) {
        char picfile[kMaxPath];
        if (!build_picture_path(ent->d_name, picfile, sizeof(picfile))) {
            continue;
        }
        if (!is_regular_file(picfile) || !is_jpeg_file(ent->d_name)) {
            continue;
        }

        if (first_picture[0] == '\0') {
            snprintf(first_picture, sizeof(first_picture), "%s", ent->d_name);
        }

        if (use_next_file) {
            snprintf(next_picture, sizeof(next_picture), "%s", ent->d_name);
            break;
        }

        if (strcmp(ent->d_name, current_picture_name) == 0) {
            use_next_file = true;
        }
    }
    closedir(dir);

    if (next_picture[0] == '\0' && first_picture[0] != '\0') {
        snprintf(next_picture, sizeof(next_picture), "%s", first_picture);
    }
    if (next_picture[0] == '\0') {
        return false;
    }

    snprintf(name, name_size, "%s", next_picture);
    return true;
}

static void trim(char *value) {
    char *start = value;
    while (*start != '\0' && isspace(static_cast<unsigned char>(*start))) {
        ++start;
    }
    if (start != value) {
        memmove(value, start, strlen(start) + 1);
    }

    size_t length = strlen(value);
    while (length > 0 && isspace(static_cast<unsigned char>(value[length - 1]))) {
        value[--length] = '\0';
    }
}

static bool parse_limited_int(const char *value, int minimum, int maximum,
                              int *result) {
    char *end = NULL;
    long parsed = strtol(value, &end, 10);
    if (end == value || *end != '\0' || parsed < minimum || parsed > maximum) {
        return false;
    }
    *result = static_cast<int>(parsed);
    return true;
}

static bool load_remote_config(RemoteConfig *config) {
    memset(config, 0, sizeof(*config));
    config->refresh_seconds = kDefaultRefreshSeconds;
    config->retry_seconds = kDefaultRetrySeconds;
    config->retry_max_seconds = kDefaultRetryMaxSeconds;
    config->timeout_seconds = kDefaultTimeoutSeconds;
    config->wake_min_battery_percent = kDefaultWakeMinBatteryPercent;
    config->refresh_mode = REFRESH_BATTERY_SAVER;

    FILE *file = fopen(REMOTE_CONFIG_PATH, "r");
    if (file == NULL) {
        return false;
    }

    char line[kConfigLineSize];
    while (fgets(line, sizeof(line), file) != NULL) {
        trim(line);
        if (line[0] == '\0' || line[0] == '#') {
            continue;
        }

        char *equals = strchr(line, '=');
        if (equals == NULL) {
            continue;
        }
        *equals = '\0';
        char *key = line;
        char *value = equals + 1;
        trim(key);
        trim(value);

        if (strcmp(key, "url") == 0) {
            snprintf(config->url, sizeof(config->url), "%s", value);
        } else if (strcmp(key, "manifest_url") == 0 ||
                   strcmp(key, "version_url") == 0) {
            snprintf(config->manifest_url, sizeof(config->manifest_url), "%s",
                     value);
        } else if (strcmp(key, "interval_minutes") == 0) {
            int minutes;
            if (parse_limited_int(value, kMinRefreshSeconds / 60, 24 * 60,
                                  &minutes)) {
                config->refresh_seconds = minutes * 60;
            }
        } else if (strcmp(key, "retry_minutes") == 0) {
            int minutes;
            if (parse_limited_int(value, kMinRefreshSeconds / 60,
                                  kMaxPollSeconds / 60, &minutes)) {
                config->retry_seconds = minutes * 60;
            }
        } else if (strcmp(key, "retry_max_minutes") == 0) {
            int minutes;
            if (parse_limited_int(value, kMinRefreshSeconds / 60,
                                  kMaxPollSeconds / 60, &minutes)) {
                config->retry_max_seconds = minutes * 60;
            }
        } else if (strcmp(key, "refresh_mode") == 0) {
            if (strcmp(value, "always_on") == 0) {
                config->refresh_mode = REFRESH_ALWAYS_ON;
            } else if (strcmp(value, "scheduled_sleep") == 0) {
                config->refresh_mode = REFRESH_SCHEDULED_SLEEP;
            } else if (strcmp(value, "battery_saver") == 0) {
                config->refresh_mode = REFRESH_BATTERY_SAVER;
            }
        } else if (strcmp(key, "wake_min_battery_percent") == 0) {
            parse_limited_int(value, 0, 100,
                              &config->wake_min_battery_percent);
        } else if (strcmp(key, "timeout_seconds") == 0) {
            parse_limited_int(value, kMinTimeoutSeconds, kMaxTimeoutSeconds,
                              &config->timeout_seconds);
        }
    }
    fclose(file);

    if (config->retry_max_seconds < config->retry_seconds) {
        config->retry_max_seconds = config->retry_seconds;
    }

    // LAN HTTP avoids TLS initialization cost and certificate compatibility issues.
    if (strncmp(config->url, "http://", 7) != 0 || config->url[7] == '\0') {
        return false;
    }
    return config->manifest_url[0] == '\0' ||
           (strncmp(config->manifest_url, "http://", 7) == 0 &&
            config->manifest_url[7] != '\0');
}

static unsigned long long hash_bytes(const unsigned char *data, size_t size) {
    unsigned long long hash = 1469598103934665603ULL;
    for (size_t i = 0; i < size; ++i) {
        hash ^= data[i];
        hash *= 1099511628211ULL;
    }
    return hash;
}

static bool hash_file(const char *path, unsigned long long *hash,
                      size_t *size) {
    FILE *file = fopen(path, "rb");
    if (file == NULL) {
        return false;
    }

    unsigned long long value = 1469598103934665603ULL;
    size_t total = 0;
    unsigned char buffer[1024];
    size_t read;
    while ((read = fread(buffer, 1, sizeof(buffer), file)) > 0) {
        for (size_t i = 0; i < read; ++i) {
            value ^= buffer[i];
            value *= 1099511628211ULL;
        }
        total += read;
    }
    bool success = !ferror(file);
    fclose(file);
    if (!success) {
        return false;
    }
    *hash = value;
    *size = total;
    return true;
}

static bool looks_like_jpeg(const unsigned char *data, int size) {
    return size >= 4 && data[0] == 0xff && data[1] == 0xd8 &&
           data[size - 2] == 0xff && data[size - 1] == 0xd9;
}

static bool save_remote_picture(const void *data, int size) {
    FILE *file = fopen(REMOTE_TEMP_PATH, "wb");
    if (file == NULL) {
        return false;
    }
    bool success = fwrite(data, 1, size, file) == static_cast<size_t>(size);
    if (fclose(file) != 0) {
        success = false;
    }
    if (!success) {
        remove(REMOTE_TEMP_PATH);
    }
    return success;
}

static bool parse_manifest(char *body, RemoteManifest *manifest) {
    memset(manifest, 0, sizeof(*manifest));
    manifest->update_ready = true;
    char *line = body;
    while (line != NULL && *line != '\0') {
        char *next_line = strchr(line, '\n');
        if (next_line != NULL) {
            *next_line = '\0';
        }
        trim(line);
        if (line[0] != '\0' && line[0] != '#') {
            char *equals = strchr(line, '=');
            if (equals == NULL) {
                // A plain response remains compatible with the old version_url.
                if (manifest->revision[0] == '\0') {
                    snprintf(manifest->revision, sizeof(manifest->revision),
                             "%s", line);
                }
            } else {
                *equals = '\0';
                char *key = line;
                char *value = equals + 1;
                trim(key);
                trim(value);
                if (strcmp(key, "revision") == 0) {
                    snprintf(manifest->revision, sizeof(manifest->revision),
                             "%s", value);
                } else if (strcmp(key, "publication_id") == 0) {
                    snprintf(manifest->publication_id,
                             sizeof(manifest->publication_id), "%s", value);
                } else if (strcmp(key, "publication_slot") == 0) {
                    char *end = NULL;
                    long parsed = strtol(value, &end, 10);
                    if (end != value && *end == '\0' && parsed > 0) {
                        manifest->publication_slot = parsed;
                    }
                } else if (strcmp(key, "update_ready") == 0) {
                    int parsed = 0;
                    if (parse_limited_int(value, 0, 1, &parsed)) {
                        manifest->has_update_ready = true;
                        manifest->update_ready = parsed == 1;
                    }
                } else if (strcmp(key, "next_poll_seconds") == 0) {
                    parse_limited_int(value, kMinRefreshSeconds,
                                      kMaxPollSeconds,
                                      &manifest->next_poll_seconds);
                } else if (strcmp(key, "retry_after_seconds") == 0) {
                    parse_limited_int(value, kMinRefreshSeconds,
                                      kMaxPollSeconds,
                                      &manifest->retry_after_seconds);
                }
            }
        }
        line = next_line == NULL ? NULL : next_line + 1;
    }
    return manifest->revision[0] != '\0';
}

static bool download_manifest(RemoteManifest *manifest) {
    int received_size = 0;
    void *received = QuickDownloadExt(remote_config.manifest_url, &received_size,
                                      remote_config.timeout_seconds, NULL, NULL);
    if (received == NULL || received_size <= 0 ||
        received_size >= kConfigLineSize) {
        free(received);
        return false;
    }

    char body[kConfigLineSize];
    memcpy(body, received, received_size);
    body[received_size] = '\0';
    free(received);
    return parse_manifest(body, manifest);
}

static void load_remote_state() {
    FILE *file = fopen(REMOTE_STATE_PATH, "r");
    if (file == NULL) {
        return;
    }
    char line[kMaxRevisionSize + 16];
    while (fgets(line, sizeof(line), file) != NULL) {
        trim(line);
        const char *prefix = "revision=";
        if (strncmp(line, prefix, strlen(prefix)) == 0) {
            snprintf(remote_revision, sizeof(remote_revision), "%s",
                     line + strlen(prefix));
            have_remote_revision = remote_revision[0] != '\0';
            continue;
        }
        prefix = "publication_id=";
        if (strncmp(line, prefix, strlen(prefix)) == 0) {
            snprintf(remote_publication_id, sizeof(remote_publication_id),
                     "%s", line + strlen(prefix));
            have_remote_publication = remote_publication_id[0] != '\0';
            continue;
        }
        prefix = "publication_slot=";
        if (strncmp(line, prefix, strlen(prefix)) == 0) {
            char *end = NULL;
            long value = strtol(line + strlen(prefix), &end, 10);
            if (end != line + strlen(prefix) && *end == '\0' && value > 0) {
                remote_publication_slot = value;
            }
            continue;
        }
        prefix = "last_update=";
        if (strncmp(line, prefix, strlen(prefix)) == 0) {
            char *end = NULL;
            long value = strtol(line + strlen(prefix), &end, 10);
            if (end != line + strlen(prefix) && *end == '\0' && value > 0) {
                last_image_update_time = static_cast<time_t>(value);
            }
        }
    }
    fclose(file);
}

static bool save_remote_state() {
    if (!have_remote_revision && !have_remote_publication &&
        last_image_update_time == 0) {
        return true;
    }
    FILE *file = fopen(REMOTE_STATE_TEMP_PATH, "w");
    if (file == NULL) {
        return false;
    }
    bool success = true;
    if (have_remote_revision) {
        success = fprintf(file, "revision=%s\n", remote_revision) > 0;
    }
    if (success && have_remote_publication) {
        success = fprintf(file, "publication_id=%s\n", remote_publication_id) > 0;
    }
    if (success && remote_publication_slot > 0) {
        success = fprintf(file, "publication_slot=%ld\n",
                          remote_publication_slot) > 0;
    }
    if (success && last_image_update_time > 0) {
        success = fprintf(file, "last_update=%ld\n",
                          static_cast<long>(last_image_update_time)) > 0;
    }
    if (fclose(file) != 0) {
        success = false;
    }
    if (!success || rename(REMOTE_STATE_TEMP_PATH, REMOTE_STATE_PATH) != 0) {
        remove(REMOTE_STATE_TEMP_PATH);
        return false;
    }
    return true;
}

static void schedule_remote_refresh(int milliseconds) {
    ClearTimer(refresh_remote_picture);
    SetWeakTimer("PocketFrameRefresh", refresh_remote_picture, milliseconds);
    remote_refresh_scheduled = true;
}

static void clear_remote_refresh() {
    ClearTimer(refresh_remote_picture);
    remote_refresh_scheduled = false;
}

static const char *refresh_mode_name(RefreshMode mode) {
    if (mode == REFRESH_ALWAYS_ON) {
        return "always_on";
    }
    if (mode == REFRESH_SCHEDULED_SLEEP) {
        return "scheduled_sleep";
    }
    return "battery_saver";
}

static bool battery_is_below_wake_threshold() {
    return remote_config.wake_min_battery_percent > 0 && !IsCharging() &&
           GetBatteryPower() <= remote_config.wake_min_battery_percent;
}

static void clear_scheduled_sleep() {
    ClearTimer(enter_scheduled_sleep);
    pending_sleep_seconds = 0;
    next_wake_time = 0;
    iv_sleepmode(0);
}

static void schedule_scheduled_sleep(int delay_seconds) {
    ClearTimer(enter_scheduled_sleep);
    if (!remote_mode || diagnostics_visible ||
        remote_config.refresh_mode != REFRESH_SCHEDULED_SLEEP) {
        return;
    }
    if (scheduled_sleep_disabled) {
        iv_sleepmode(1);
        return;
    }

    scheduled_sleep_low_battery = battery_is_below_wake_threshold();
    if (scheduled_sleep_low_battery) {
        delay_seconds = kMaxPollSeconds;
    }
    if (delay_seconds < kMinRefreshSeconds) {
        delay_seconds = kMinRefreshSeconds;
    }
    if (delay_seconds > kMaxPollSeconds) {
        delay_seconds = kMaxPollSeconds;
    }

    pending_sleep_seconds = delay_seconds;
    next_wake_time = time(NULL) + delay_seconds +
                     (kSleepSettleMilliseconds + 999) / 1000;
    SetWeakTimer("PocketFrameSleep", enter_scheduled_sleep,
                 kSleepSettleMilliseconds);
}

static void enter_scheduled_sleep() {
    if (!remote_mode || diagnostics_visible || scheduled_sleep_disabled ||
        remote_config.refresh_mode != REFRESH_SCHEDULED_SLEEP ||
        pending_sleep_seconds <= 0) {
        return;
    }

    int requested_seconds = pending_sleep_seconds;
    release_network();
    last_sleep_started_time = time(NULL);
    next_wake_time = last_sleep_started_time + requested_seconds;
    iv_sleepmode(1);
    last_sleep_result = GoSleep(requested_seconds * 1000, 0);
    iv_sleepmode(0);
    last_sleep_finished_time = time(NULL);
    last_sleep_elapsed_seconds = static_cast<int>(
        last_sleep_finished_time - last_sleep_started_time);
    last_wake_likely_rtc =
        last_sleep_elapsed_seconds >= requested_seconds - 5;
    suppress_next_repaint = true;
    pending_sleep_seconds = 0;
    next_wake_time = 0;

    if (last_sleep_elapsed_seconds < kImmediateSleepFailureSeconds) {
        suppress_next_repaint = false;
        ++immediate_sleep_failures;
        if (immediate_sleep_failures >= kMaxImmediateSleepFailures) {
            scheduled_sleep_disabled = true;
            iv_sleepmode(1);
        } else {
            schedule_scheduled_sleep(requested_seconds);
        }
        return;
    }
    immediate_sleep_failures = 0;

    if (scheduled_sleep_low_battery && last_wake_likely_rtc &&
        battery_is_below_wake_threshold()) {
        schedule_scheduled_sleep(kMaxPollSeconds);
        return;
    }
    schedule_remote_refresh(kWakeSettleMilliseconds);
}

static void format_diagnostic_time(time_t value, char *text, size_t text_size) {
    if (value <= 0) {
        snprintf(text, text_size, "not available");
        return;
    }

    struct tm *local = localtime(&value);
    if (local == NULL || strftime(text, text_size, "%Y-%m-%d %H:%M", local) == 0) {
        snprintf(text, text_size, "not available");
    }
}

static void draw_diagnostics() {
    int panel_width = ScreenWidth() / 2;
    int panel_height = ScreenHeight() / 2;
    int panel_x = (ScreenWidth() - panel_width) / 2;
    int panel_y = (ScreenHeight() - panel_height) / 2;
    int margin = kFontSize * 2;
    int text_width = panel_width - margin * 2;
    int line_height = kFontSize * 2;
    int y = panel_y + margin;
    char now[32];
    char updated[32];
    char checked[32];
    char last_success[32];
    char wake[32];
    char line[64];

    format_diagnostic_time(time(NULL), now, sizeof(now));
    format_diagnostic_time(last_image_update_time, updated, sizeof(updated));
    format_diagnostic_time(last_network_attempt_time, checked, sizeof(checked));
    format_diagnostic_time(last_network_success_time, last_success,
                           sizeof(last_success));
    format_diagnostic_time(next_wake_time, wake, sizeof(wake));

    FillArea(panel_x, panel_y, panel_width, panel_height, WHITE);
    DrawRect(panel_x, panel_y, panel_width - 1, panel_height - 1, BLACK);
    DrawTextRect(panel_x + margin, y, text_width, line_height, "PocketFrame",
                 ALIGN_CENTER);
    y += line_height * 2;

    snprintf(line, sizeof(line), "Time: %s", now);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Updated: %s", updated);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Battery: %d%%", GetBatteryPower());
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Mode: %s",
             refresh_mode_name(remote_config.refresh_mode));
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Next wake: %s", wake);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Checked: %s", checked);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Last OK: %s", last_success);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Network: %s, %ds",
             last_network_attempt_time == 0 ? "none" :
             (last_network_succeeded ? "OK" : "failed"),
             last_network_duration_seconds);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Checks: %d OK, %d failed",
             successful_network_checks, failed_network_checks);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Failures in row: %d",
             consecutive_network_failures);
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Sleep: %ds rc=%d %s",
             last_sleep_elapsed_seconds, last_sleep_result,
             last_sleep_started_time == 0 ? "none" :
             (last_wake_likely_rtc ? "RTC" : "manual"));
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);
    y += line_height;
    snprintf(line, sizeof(line), "Sleep guard: %s, battery: %s",
             scheduled_sleep_disabled ? "disabled" : "OK",
             scheduled_sleep_low_battery ? "low" : "OK");
    DrawTextRect(panel_x + margin, y, text_width, line_height, line, ALIGN_LEFT);

    // The dialog is only black and white, so the low-energy E-Ink update is enough.
    PartialUpdateBW(panel_x, panel_y, panel_width, panel_height);
}

static void open_diagnostics() {
    ClearTimer(show_next_picture);
    clear_remote_refresh();
    clear_scheduled_sleep();
    diagnostics_visible = true;
    draw_diagnostics();
}

static void close_diagnostics() {
    diagnostics_visible = false;
    repaint_current_picture();
    if (remote_mode &&
        remote_config.refresh_mode == REFRESH_SCHEDULED_SLEEP) {
        clear_remote_refresh();
        schedule_remote_refresh(100);
    }
}

static void release_network() {
    NetDisconnect();
}

static bool apply_manifest_publication(const RemoteManifest *manifest) {
    if (manifest->publication_id[0] == '\0') {
        return false;
    }
    bool changed = !have_remote_publication ||
                   strcmp(remote_publication_id,
                          manifest->publication_id) != 0 ||
                   remote_publication_slot != manifest->publication_slot;
    snprintf(remote_publication_id, sizeof(remote_publication_id), "%s",
             manifest->publication_id);
    have_remote_publication = true;
    remote_publication_slot = manifest->publication_slot;
    return changed;
}

static bool refresh_remote_picture_now() {
    RemoteConfig loaded_config;
    if (!load_remote_config(&loaded_config)) {
        return false;
    }
    remote_config = loaded_config;
    if (remote_config.refresh_mode != REFRESH_SCHEDULED_SLEEP) {
        iv_sleepmode(0);
    }
    remote_next_poll_seconds = remote_config.refresh_seconds;
    remote_retry_seconds = remote_config.retry_seconds;
    remote_retry_max_seconds = remote_config.retry_max_seconds;

    if ((QueryNetwork() & NET_CONNECTED) == 0) {
        // Passing NULL asks InkView to use the already configured default Wi-Fi.
        if (NetConnect(NULL) != NET_OK) {
            release_network();
            return false;
        }
    }

    RemoteManifest manifest;
    memset(&manifest, 0, sizeof(manifest));
    bool use_manifest = remote_config.manifest_url[0] != '\0';
    if (use_manifest) {
        if (!download_manifest(&manifest)) {
            release_network();
            return false;
        }
        if (manifest.next_poll_seconds > 0) {
            remote_next_poll_seconds = manifest.next_poll_seconds;
        }
        if (manifest.retry_after_seconds > 0) {
            remote_retry_seconds = manifest.retry_after_seconds;
        }
        if (manifest.has_update_ready && !manifest.update_ready) {
            release_network();
            return true;
        }
        if (have_remote_revision &&
            strcmp(manifest.revision, remote_revision) == 0) {
            if (apply_manifest_publication(&manifest)) {
                save_remote_state();
            }
            release_network();
            return true;
        }
    }

    int received_size = 0;
    void *received = QuickDownloadExt(remote_config.url, &received_size,
                                      remote_config.timeout_seconds, NULL, NULL);
    release_network();

    if (received == NULL || !looks_like_jpeg(
                                static_cast<unsigned char *>(received),
                                received_size)) {
        free(received);
        return false;
    }

    unsigned long long received_hash =
        hash_bytes(static_cast<unsigned char *>(received), received_size);
    if (have_remote_hash && remote_hash == received_hash &&
        remote_size == static_cast<size_t>(received_size)) {
        bool state_changed = false;
        if (use_manifest) {
            if (!have_remote_revision ||
                strcmp(remote_revision, manifest.revision) != 0) {
                snprintf(remote_revision, sizeof(remote_revision), "%s",
                         manifest.revision);
                have_remote_revision = true;
                state_changed = true;
            }
            if (apply_manifest_publication(&manifest)) {
                state_changed = true;
            }
        }
        if (state_changed) {
            save_remote_state();
        }
        free(received);
        return true;
    }

    bool saved = save_remote_picture(received, received_size);
    free(received);
    if (!saved) {
        return false;
    }

    // Decode before replacing the cache, so a bad response never replaces a good frame.
    if (!load_and_draw_path(REMOTE_TEMP_PATH, false)) {
        remove(REMOTE_TEMP_PATH);
        return false;
    }
    if (rename(REMOTE_TEMP_PATH, REMOTE_CACHE_PATH) != 0) {
        remove(REMOTE_TEMP_PATH);
        return false;
    }

    remote_hash = received_hash;
    remote_size = static_cast<size_t>(received_size);
    have_remote_hash = true;
    if (use_manifest) {
        snprintf(remote_revision, sizeof(remote_revision), "%s",
                 manifest.revision);
        have_remote_revision = true;
        apply_manifest_publication(&manifest);
    }
    last_image_update_time = time(NULL);
    save_remote_state();
    return true;
}

static void refresh_remote_picture() {
    if (!remote_mode || diagnostics_visible) {
        return;
    }
    remote_refresh_scheduled = false;
    suppress_next_repaint = false;
    time_t started = time(NULL);
    last_network_attempt_time = started;
    bool success = refresh_remote_picture_now();
    time_t finished = time(NULL);
    last_network_duration_seconds =
        finished > started ? static_cast<int>(finished - started) : 0;
    last_network_succeeded = success;

    if (success) {
        ++successful_network_checks;
        consecutive_network_failures = 0;
        last_network_success_time = finished;
        if (remote_config.refresh_mode == REFRESH_ALWAYS_ON) {
            schedule_remote_refresh(remote_next_poll_seconds * 1000);
        } else if (remote_config.refresh_mode == REFRESH_SCHEDULED_SLEEP) {
            schedule_scheduled_sleep(remote_next_poll_seconds);
        }
        return;
    }

    ++failed_network_checks;
    ++consecutive_network_failures;
    int delay_seconds = remote_retry_seconds;
    for (int attempt = 1; attempt < consecutive_network_failures;
         ++attempt) {
        if (delay_seconds >= remote_retry_max_seconds / 2) {
            delay_seconds = remote_retry_max_seconds;
            break;
        }
        delay_seconds *= 2;
    }
    if (delay_seconds > remote_retry_max_seconds) {
        delay_seconds = remote_retry_max_seconds;
    }

    // With a cached frame, battery saver waits for foreground or a manual
    // request. It still retries an empty frame so first setup can recover.
    if (remote_config.refresh_mode == REFRESH_SCHEDULED_SLEEP) {
        schedule_scheduled_sleep(delay_seconds);
    } else if (remote_config.refresh_mode == REFRESH_ALWAYS_ON ||
               !is_regular_file(REMOTE_CACHE_PATH)) {
        schedule_remote_refresh(delay_seconds * 1000);
    }
}

static void schedule_next_picture(int ms) {
    ClearTimer(show_next_picture);
    SetWeakTimer("PocketFrameNext", show_next_picture, ms);
}

static void show_next_picture() {
    char next_picture[kMaxPath];
    if (picture_dir == NULL || diagnostics_visible) {
        return;
    }

    if (!find_next_picture(next_picture, sizeof(next_picture))) {
        show_status("PocketFrame: no JPEG files found", picture_dir);
        return;
    }

    snprintf(current_picture_name, sizeof(current_picture_name), "%s",
             next_picture);
    if (load_and_draw_picture(current_picture_name)) {
        schedule_next_picture(PICTURE_DISPLAY_TIME * 1000);
    } else {
        schedule_next_picture(5000);
    }
}

static void start_remote_frame() {
    unsigned long long cached_hash;
    size_t cached_size;
    if (is_regular_file(REMOTE_CACHE_PATH) &&
        hash_file(REMOTE_CACHE_PATH, &cached_hash, &cached_size)) {
        remote_hash = cached_hash;
        remote_size = cached_size;
        have_remote_hash = true;
        load_and_draw_path(REMOTE_CACHE_PATH, false);
    } else {
        show_status("PocketFrame: waiting for LAN image", "");
    }
    load_remote_state();

    // Let the first paint finish before a potentially slow Wi-Fi connection.
    schedule_remote_refresh(100);
}

static void repaint_current_picture() {
    if (remote_mode) {
        if (is_regular_file(REMOTE_CACHE_PATH)) {
            load_and_draw_path(REMOTE_CACHE_PATH, false);
        } else if (!diagnostics_visible) {
            schedule_remote_refresh(100);
        }
        if (!diagnostics_visible &&
            remote_config.refresh_mode == REFRESH_ALWAYS_ON &&
            !remote_refresh_scheduled) {
            schedule_remote_refresh(remote_config.refresh_seconds * 1000);
        }
        if (diagnostics_visible) {
            draw_diagnostics();
        }
        return;
    }

    if (diagnostics_visible) {
        if (current_picture_name[0] != '\0') {
            load_and_draw_picture(current_picture_name);
        }
        draw_diagnostics();
        return;
    }

    if (picture_dir == NULL) {
        return;
    }
    if (current_picture_name[0] == '\0') {
        show_next_picture();
        return;
    }
    if (load_and_draw_picture(current_picture_name)) {
        schedule_next_picture(PICTURE_DISPLAY_TIME * 1000);
    } else {
        show_next_picture();
    }
}

static int main_handler(int event_type, int param_one, int param_two) {
    if (EVT_INIT == event_type) {
        font = OpenFont("LiberationSans", kFontSize, 0);
        SetFont(font, BLACK);
        y_log = 0;
        ClearScreen();
        FullUpdate();

        remote_mode = load_remote_config(&remote_config);
        if (remote_mode) {
            start_remote_frame();
            return 0;
        }

        picture_dir = find_picture_dir();
        if (picture_dir == NULL) {
            show_status("PocketFrame: no configuration or picture folder",
                        "Add system/config/pocketframe.cfg for LAN mode.");
            return 0;
        }

        current_picture_name[0] = '\0';
        show_next_picture();
    } else if (EVT_SHOW == event_type || EVT_REPAINT == event_type) {
        if (suppress_next_repaint) {
            suppress_next_repaint = false;
        } else {
            repaint_current_picture();
        }
    } else if (EVT_FOREGROUND == event_type || EVT_ACTIVATE == event_type) {
        clear_scheduled_sleep();
        if (suppress_next_repaint) {
            suppress_next_repaint = false;
        } else {
            repaint_current_picture();
        }
        if (remote_mode && !diagnostics_visible) {
            clear_remote_refresh();
            schedule_remote_refresh(100);
        }
    } else if (EVT_HIDE == event_type || EVT_BACKGROUND == event_type) {
        ClearTimer(show_next_picture);
        clear_remote_refresh();
        clear_scheduled_sleep();
        release_network();
    } else if (EVT_EXIT == event_type) {
        ClearTimer(show_next_picture);
        clear_remote_refresh();
        clear_scheduled_sleep();
        CloseFont(font);
    } else if (EVT_KEYPRESS == event_type) {
        if (param_one == KEY_MENU) {
            if (diagnostics_visible) {
                close_diagnostics();
            } else {
                open_diagnostics();
            }
        } else if (remote_mode &&
                   (param_one == KEY_RIGHT || param_one == KEY_NEXT ||
                    param_one == KEY_NEXT2 || param_one == KEY_OK)) {
            if (diagnostics_visible) {
                close_diagnostics();
            }
            clear_remote_refresh();
            clear_scheduled_sleep();
            schedule_remote_refresh(100);
        } else if (param_one == KEY_BACK || param_one == KEY_HOME) {
            CloseApp();
        }
    }
    return 0;
}

int main(int argc, char *argv[]) {
    InkViewMain(main_handler);
    return 0;
}
