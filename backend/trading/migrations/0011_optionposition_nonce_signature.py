from django.db import migrations, models


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0010_option_position'),
    ]

    operations = [
        migrations.AddField(
            model_name='optionposition',
            name='nonce',
            field=models.CharField(blank=True, max_length=64, null=True, unique=True),
        ),
        migrations.AddField(
            model_name='optionposition',
            name='signature',
            field=models.TextField(blank=True, null=True),
        ),
    ]
