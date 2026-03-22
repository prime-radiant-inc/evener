def fibonacci(n):
    if n <= 0:
        return []

    fib_numbers = []
    a, b = 0, 1

    for _ in range(n):
        fib_numbers.append(a)
        a, b = b, a + b

    return fib_numbers


if __name__ == "__main__":
    print(fibonacci(10))
