#!/usr/bin/python3

import os, sys

def parse_memory_from_sar(filename):
    mem = []
    i=0
    for line in open(filename, 'r'):
        i += 1
        # First three lines are a comment.
        if i<=3:
            continue

        vals = line.split()
        if len(vals)>3:
            mem.append(int(vals[3]))

    return mem

if len(sys.argv) < 2:
    print("Usage "+sys.argv[0]+" <BENCH RESULTS DIR>")
    exit(1)

BENCH_DIR = sys.argv[1]

for filename in os.listdir(BENCH_DIR):
    chunks = filename.split('.')

    test_name = chunks[0]
    param = dict()
    for i in range(1,len(chunks)-1):
        t, v = chunks[i].split('_')
        param[t] = v

    bench_type = chunks[-1]

    if bench_type == "sar":
        mem = parse_memory_from_sar(BENCH_DIR+"/"+filename)
        if len(mem)>0:
            print("test "+test_name+" with params "+str(param)+" max mem usage: "+str(int(sorted(mem)[-1])/(1024))+" MiB")
