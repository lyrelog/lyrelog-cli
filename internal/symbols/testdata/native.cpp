// The test library for elf_test.go: a little C++ with what a game's native code has (inlined
// calls, templates, a class, a lambda), built for Android (see elf_test.go).
#include <cstdint>
#include <cstddef>

namespace tilefall {

struct Cell {
    int value;
    Cell* next;
};

template <typename T>
static inline T clampTo(T v, T lo, T hi) {
    return v < lo ? lo : (v > hi ? hi : v);
}

class Board {
public:
    explicit Board(Cell* first) : first_(first) {}

    __attribute__((noinline)) int sum(int limit) const {
        int total = 0;
        for (Cell* c = first_; c != nullptr; c = c->next) {
            total += clampTo(c->value, 0, limit);
        }
        return total;
    }

    static inline int deref(const Cell* c) { return c->value * 3; }

    __attribute__((noinline)) int step(const Cell* c) const {
        auto twice = [](int v) { return v * 2 + 1; };
        return twice(deref(c)) + sum(9);
    }

private:
    Cell* first_;
};

} // namespace tilefall

extern "C" __attribute__((visibility("default"))) int tilefall_step(void* first, void* cell) {
    tilefall::Board board(static_cast<tilefall::Cell*>(first));
    return board.step(static_cast<const tilefall::Cell*>(cell));
}

extern "C" __attribute__((visibility("default"))) int tilefall_crash(void* first) {
    return tilefall_step(first, nullptr); // dereferences nullptr in Board::deref
}
