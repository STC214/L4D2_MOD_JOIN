from pathlib import Path
import sys

from PIL import Image, ImageEnhance, ImageFilter


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    source = root / "ico.jpg"
    output_dir = root / "assets"
    output_dir.mkdir(parents=True, exist_ok=True)
    preview = output_dir / "app-icon.png"
    icon = output_dir / "app.ico"

    if not source.exists():
        raise SystemExit(f"Missing icon source: {source}")

    with Image.open(source) as image:
        image = image.convert("RGB")
        width, height = image.size

        # Keep the face, hair, raised fist and pink aura readable at small sizes.
        crop_size = min(width, int(height * 0.67))
        left = max(0, (width - crop_size) // 2)
        top = max(0, int(height * 0.025))
        if top + crop_size > height:
            top = height - crop_size
        image = image.crop((left, top, left + crop_size, top + crop_size))
        image = image.resize((1024, 1024), Image.Resampling.LANCZOS)
        image = ImageEnhance.Contrast(image).enhance(1.04)
        image = ImageEnhance.Color(image).enhance(1.04)
        image = image.filter(ImageFilter.UnsharpMask(radius=1.2, percent=115, threshold=3))

        image.save(preview, "PNG", optimize=True)
        image.save(
            icon,
            format="ICO",
            sizes=[
                (16, 16),
                (20, 20),
                (24, 24),
                (32, 32),
                (40, 40),
                (48, 48),
                (64, 64),
                (128, 128),
                (256, 256),
            ],
        )

    print(f"Built icon: {icon}")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"Icon build failed: {exc}", file=sys.stderr)
        raise
