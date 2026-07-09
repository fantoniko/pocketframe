#include <dirent.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cctype>
#include <cstdio>
#include <cstdlib>
#include <cstring>

#include "inkview.h"

static ifont *font;
static int y_log;
static bool debug = false;
static const int kFontSize = 12;
static const int PICTURE_DISPLAY_TIME = 7200;  // 2 hours
static const char *PICTURE_DIRS[] = {
    "/mnt/ext1/My pictures/PocketFrame/",
    "/mnt/ext1/My Pictures/PocketFrame/",
    "/mnt/ext2/My pictures/PocketFrame/",
    "/mnt/ext2/My Pictures/PocketFrame/",
};

static void log_message(const char *msg) {
    if (!debug) {
        return;
    }
    if (strlen(msg) == 0) {
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

static int main_handler(int event_type, int param_one, int param_two) {
    if (EVT_INIT == event_type) {
        font = OpenFont("LiberationSans", kFontSize, 0);
        SetFont(font, BLACK);
        y_log = 0;
        ClearScreen();
        FullUpdate();

        const char *picture_dir = find_picture_dir();
        if (picture_dir == NULL) {
            show_status("PocketFrame: no picture folder",
                        "Create My pictures/PocketFrame on internal storage "
                        "or SD card.");
            return 0;
        }

        // Read the content of a directory
        // https://stackoverflow.com/a/612176
        DIR *dir;
        struct dirent *ent;
        int pictures_shown = 0;
        int pictures_failed = 0;
        if ((dir = opendir(picture_dir)) != NULL) {
            // Print all the files and directories within directory
            while ((ent = readdir(dir)) != NULL) {
                char picfile[300];
                snprintf(picfile, sizeof(picfile), "%s%s", picture_dir,
                         ent->d_name);

                if (!is_regular_file(picfile) || !is_jpeg_file(ent->d_name)) {
                    log_message(picfile);
                    continue;
                }

                // Load picture and write it to a buffer
                ibitmap *picture = LoadJPEG(picfile, ScreenWidth(), ScreenHeight(),
                                            100, 100, 1);
                if (picture == NULL || picture->data == NULL) {
                    ++pictures_failed;
                    show_status("PocketFrame: JPEG load failed", picfile);
                    sleep(5);
                    continue;
                }

                Stretch(picture->data, IMAGE_GRAY2, picture->width,
                        picture->height, picture->scanline, 0, 0, ScreenWidth(),
                        ScreenHeight(), 0);
                log_message(picfile);
                ++pictures_shown;

                // Copy buffer to the real screen
                FullUpdate();
                sleep(PICTURE_DISPLAY_TIME);
            }
            closedir(dir);
        } else {
            // Could not open directory
            show_status("PocketFrame: could not open folder", picture_dir);
            return 0;
        }

        if (pictures_shown == 0) {
            if (pictures_failed > 0) {
                show_status("PocketFrame: no readable JPEG files", picture_dir);
            } else {
                show_status("PocketFrame: no JPEG files found", picture_dir);
            }
        } else {
            show_status("PocketFrame: picture stream ended",
                        "Press any key to close.");
        }

        CloseFont(font);
    } else if (EVT_KEYPRESS == event_type) {
        CloseApp();
    }
    return 0;
}

int main(int argc, char *argv[]) {
    InkViewMain(main_handler);
    return 0;
}
