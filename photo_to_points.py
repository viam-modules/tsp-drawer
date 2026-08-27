from PIL import Image, ImageEnhance
import matplotlib.pyplot as plt
import random
import os
import sys

def photo_to_points(img_path, target_resolution=(100, 100), contrast_enhance=3., show_lines=False):
    ''' Converts a photo to a pointilism representation of the photo. Prints the new image and outputs the coordinates for the points '''

    im = Image.open(img_path)
    im = ImageEnhance.Contrast(im)
    im = im.enhance(contrast_enhance)
    im = im.resize(target_resolution)
    im = im.convert("L") # greyscale

    x_vals = []
    y_vals = []

    for x in range(target_resolution[0]):
        for y in range(target_resolution[1]):
            # get color value of pixel and use it to determine how many dots to draw
            value = im.getpixel((x, y)) # should be a number
            pixel_weight = 2*(255-value)//((target_resolution[0]+target_resolution[1])//2) 

            for _ in range(pixel_weight):
                x_vals += [x + random.random()]
                y_vals += [target_resolution[1] - (y + random.random())]

    plt.figure()
    split_path = list(os.path.split(img_path))

    if show_lines:
        plt.plot(x_vals, y_vals) 
        split_path[len(split_path)-1] = "line-"+split_path[len(split_path)-1]
    else:
        plt.scatter(x_vals, y_vals, s=3)
        split_path[len(split_path)-1] = "pointilism-"+split_path[len(split_path)-1]

    # plt.savefig(os.path.join(*split_path)) 
  
    plt.show()
    return (x_vals, y_vals)

# Receive data from TypeScript
file = sys.stdin.read()
result = photo_to_points(file, contrast_enhance=3, target_resolution=(100, 100))   
print(result)
     