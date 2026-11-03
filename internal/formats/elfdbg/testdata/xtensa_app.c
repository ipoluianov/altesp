// The source of xtensa_app.elf, a test fixture of the elfdbg package (MIT,
// part of AltEsp). No ESP-IDF: a chain of calls to unwind and an inlined
// function to symbolize. Built by build_xtensa.sh with xtensa-esp32-elf-gcc.

volatile int sink;
int *volatile bad; // zero: the store through it faults

// Stand-ins for the task lists of FreeRTOS (tasks.c), found by their names
__attribute__((used)) int pxReadyTasksLists[25 * 5];
__attribute__((used)) int xDelayedTaskList1[5], xSuspendedTaskList[5];

static inline __attribute__((always_inline)) void store(int v)
{
	sink = v; // LINE_STORE
}

__attribute__((noinline)) void crash(int v)
{
	store(v * 3);
	*bad = v; // LINE_CRASH
}

__attribute__((noinline)) void level2(int v)
{
	crash(v + 1); // LINE_CALL_CRASH
	sink = 2;
}

__attribute__((noinline)) void level1(int v)
{
	level2(v + 1); // LINE_CALL_LEVEL2
	sink = 1;
}

void app_main(void)
{
	level1(1); // LINE_CALL_LEVEL1
	sink = 0;
}
