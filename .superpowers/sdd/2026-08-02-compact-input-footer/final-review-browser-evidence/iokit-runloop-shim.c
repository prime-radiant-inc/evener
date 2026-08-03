#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>

// The fixed Serf sandbox denies the IOKit registration used by Chromium's
// power monitor. Chromium still calls IONotificationPortGetRunLoopSource on the
// unusable notification port and crashes inside IOKit. Return a valid inert
// source so the browser can proceed; no product code loads this interposer.
static void inert_perform(void *info) { (void)info; }

static CFRunLoopSourceRef replacement_IONotificationPortGetRunLoopSource(
    IONotificationPortRef notify) {
  (void)notify;
  CFRunLoopSourceContext context = {0};
  context.perform = inert_perform;
  return CFRunLoopSourceCreate(kCFAllocatorDefault, 0, &context);
}

__attribute__((used)) static struct {
  const void *replacement;
  const void *replacee;
} interposers[] __attribute__((section("__DATA,__interpose"))) = {
    {(const void *)replacement_IONotificationPortGetRunLoopSource,
     (const void *)IONotificationPortGetRunLoopSource},
};
